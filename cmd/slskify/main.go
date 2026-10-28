// Command slskify downloads Spotify playlists, albums and tracks from
// Soulseek through a user-run slskd instance.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/cmerk2021/slskify/internal/app"
	"github.com/cmerk2021/slskify/internal/config"
	"github.com/cmerk2021/slskify/internal/match"
	"github.com/cmerk2021/slskify/internal/pipeline"
	"github.com/cmerk2021/slskify/internal/store"
	"github.com/cmerk2021/slskify/internal/tui"
)

type globalFlags struct {
	configPath string
	dbPath     string
	o          config.Overrides
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	g := &globalFlags{}
	root := &cobra.Command{
		Use:   "slskify [spotify-url]",
		Short: "Download Spotify playlists/albums/tracks from Soulseek via slskd",
		Long: `slskify resolves a Spotify playlist, album or track, searches your slskd
instance for each track, auto-downloads confident matches and collects the
rest in a review queue.

Run without arguments to pick a past run or paste a URL.`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			url := ""
			if len(args) == 1 {
				url = args[0]
			}
			return runTUI(cmd.Context(), g, tui.Options{URL: url})
		},
	}
	pf := root.PersistentFlags()
	pf.StringVar(&g.configPath, "config", "", "config file (default: <user config dir>/slskify/config.toml)")
	pf.StringVar(&g.dbPath, "db", "", "state database (default: <user data dir>/slskify/state.db)")
	pf.StringVarP(&g.o.Quality, "quality", "q", "", "quality preset (e.g. lossless-first, lossless-only, mp3-best, any-best)")
	pf.StringVarP(&g.o.Speed, "speed", "s", "", "speed preset (slow, medium, fast)")
	pf.StringVar(&g.o.SlskdURL, "slskd-url", "", "slskd base URL (env SLSKD_URL)")
	pf.StringVar(&g.o.SlskdAPIKey, "slskd-api-key", "", "slskd API key (env SLSKD_API_KEY)")
	pf.StringVar(&g.o.SpotifyClientID, "spotify-client-id", "", "Spotify client ID (env SPOTIFY_CLIENT_ID)")
	pf.StringVar(&g.o.SpotifyClientSecret, "spotify-client-secret", "", "Spotify client secret (env SPOTIFY_CLIENT_SECRET)")

	root.AddCommand(&cobra.Command{
		Use:   "setup",
		Short: "Configure and test slskd and Spotify credentials",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTUI(cmd.Context(), g, tui.Options{SetupOnly: true})
		},
	})
	root.AddCommand(newDryRunCmd(g))
	root.AddCommand(&cobra.Command{
		Use:   "config-path",
		Short: "Print the config file and state database paths",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cp, dp, err := paths(g)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "config: %s\nstate:  %s\n", cp, dp)
			return nil
		},
	})
	return root
}

func paths(g *globalFlags) (string, string, error) {
	cp := g.configPath
	if cp == "" {
		p, err := config.Path()
		if err != nil {
			return "", "", err
		}
		cp = p
	}
	dp := g.dbPath
	if dp == "" {
		p, err := config.DBPath()
		if err != nil {
			return "", "", err
		}
		dp = p
	}
	return cp, dp, nil
}

func runTUI(ctx context.Context, g *globalFlags, opts tui.Options) error {
	cfg, cfgPath, err := config.Resolve(g.configPath, g.o)
	if err != nil {
		return err
	}
	_, dbPath, err := paths(g)
	if err != nil {
		return err
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("opening state database %s: %w", dbPath, err)
	}
	defer st.Close()
	return tui.Run(ctx, app.New(cfg, cfgPath, st), opts)
}

// ---- dry-run ----

func newDryRunCmd(g *globalFlags) *cobra.Command {
	var top, rejected, limit, only int
	cmd := &cobra.Command{
		Use:   "dry-run <spotify-url>",
		Short: "Resolve, search and print ranked candidates with score breakdowns (no downloads)",
		Long: `dry-run resolves the URL, runs the same multi-query search and matcher as a
real run, and prints every track's decision plus its ranked candidates with
full score breakdowns. Nothing is downloaded or saved. Use it to tune the
[match] section of the config against real data.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgPath, err := config.Resolve(g.configPath, g.o)
			if err != nil {
				return err
			}
			if miss := cfg.MissingCredentials(); len(miss) > 0 {
				return fmt.Errorf("missing %s: run `slskify setup` or set SLSKD_URL, SLSKD_API_KEY, SPOTIFY_CLIENT_ID, SPOTIFY_CLIENT_SECRET", strings.Join(miss, ", "))
			}
			a := app.New(cfg, cfgPath, nil)
			return dryRun(cmd.Context(), cmd.OutOrStdout(), a, args[0], dryRunOpts{top: top, rejected: rejected, limit: limit, only: only})
		},
	}
	cmd.Flags().IntVarP(&top, "top", "n", 5, "candidates to print per track (0 = all that passed hard filters)")
	cmd.Flags().IntVar(&rejected, "rejected", 3, "hard-rejected candidates to print per track")
	cmd.Flags().IntVar(&limit, "limit", 0, "only process the first N tracks (0 = all)")
	cmd.Flags().IntVar(&only, "track", 0, "only process track number N (1-based position in the source)")
	return cmd
}

type dryRunOpts struct{ top, rejected, limit, only int }

func dryRun(ctx context.Context, w io.Writer, a *app.App, url string, o dryRunOpts) error {
	fmt.Fprintf(w, "Resolving %s …\n", url)
	coll, err := a.Spotify.Resolve(ctx, url)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%s %q", coll.Kind, coll.Name)
	if coll.Owner != "" {
		fmt.Fprintf(w, " by %s", coll.Owner)
	}
	fmt.Fprintf(w, ": %d tracks", len(coll.Tracks))
	if len(coll.Skipped) > 0 {
		fmt.Fprintf(w, ", %d skipped", len(coll.Skipped))
	}
	fmt.Fprintln(w)
	for _, s := range coll.Skipped {
		fmt.Fprintf(w, "  skipped #%d %s: %s\n", s.Position, s.Name, s.Reason)
	}
	pol, qn := a.Policy(a.Cfg.QualityPreset)
	sp, sn := a.Speed(a.Cfg.SpeedPreset)
	fmt.Fprintf(w, "quality: %s · speed: %s (search timeout %s) · thresholds: auto ≥ %d, review ≥ %d\n",
		qn, sn, sp.SearchTimeout.Duration, a.Cfg.Match.HighThreshold, a.Cfg.Match.LowThreshold)

	if err := a.Slskd.Ping(ctx); err != nil {
		return fmt.Errorf("slskd: %w", err)
	}
	s := &pipeline.Searcher{
		Slskd: a.Slskd, Matcher: a.Matcher(), Policy: pol, Options: pipeline.SearchOptionsFor(sp),
		OnQuery: func(q string) { fmt.Fprintf(w, "       searching: %q\n", q) },
	}
	tally := map[match.Outcome]int{}
	n := 0
	for i, t := range coll.Tracks {
		if o.only > 0 && i+1 != o.only {
			continue
		}
		if o.limit > 0 && n >= o.limit {
			break
		}
		n++
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fmt.Fprintf(w, "\n[%d/%d] %s — %s\n", i+1, len(coll.Tracks), t.Title, strings.Join(t.Artists, ", "))
		fmt.Fprintf(w, "       %s", t.Album)
		if t.Year > 0 {
			fmt.Fprintf(w, " (%d)", t.Year)
		}
		fmt.Fprintf(w, " · %s · track %d · %d:%02d · ISRC %s\n", t.AlbumType, t.TrackNumber, t.DurationMs/60000, t.DurationMs/1000%60, orDash(t.ISRC))
		res, err := s.Search(ctx, pipeline.TargetOf(t), "")
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Fprintf(w, "       ERROR: %v\n", err)
			continue
		}
		printDecision(w, res, o)
		tally[res.Decision.Outcome]++
	}
	fmt.Fprintf(w, "\nSummary: %d auto, %d review, %d not found (of %d)\n",
		tally[match.OutcomeAuto], tally[match.OutcomeReview], tally[match.OutcomeNotFound], n)
	return nil
}

func printDecision(w io.Writer, res pipeline.SearchResult, o dryRunOpts) {
	d := res.Decision
	fmt.Fprintf(w, "       files: %d (%d passed hard filters, %d auto-eligible)\n", len(res.Files), d.Passed, d.AutoCount)
	fmt.Fprintf(w, "       DECISION: %s — %s\n", strings.ToUpper(string(d.Outcome)), d.Reason)
	passed := d.Candidates[:d.Passed]
	if o.top > 0 && len(passed) > o.top {
		passed = passed[:o.top]
	}
	for i, c := range passed {
		printCandidate(w, fmt.Sprintf("#%d", i+1), c)
	}
	rej := d.Candidates[d.Passed:]
	if len(rej) > o.rejected {
		rej = rej[:o.rejected]
	}
	if len(rej) > 0 {
		fmt.Fprintf(w, "     rejected by hard filters (best %d of %d):\n", len(rej), len(d.Candidates)-d.Passed)
		for _, c := range rej {
			printCandidate(w, "x", c)
		}
	}
}

func printCandidate(w io.Writer, label string, c *match.Candidate) {
	var notes []string
	if c.AutoEligible {
		notes = append(notes, "AUTO")
	}
	if c.Rejected != "" {
		notes = append(notes, "rejected: "+c.Rejected)
	}
	peer := "busy"
	if c.FreeSlot {
		peer = "free"
	}
	fmt.Fprintf(w, "  %-4s conf %3d  %-14s tier %-12s peer %s %dKB/s q%d  %s\n",
		label, c.Confidence, c.Quality.Label, c.TierName, peer, c.UploadSpeed/1024, c.QueueLength, strings.Join(notes, " · "))
	length := "?"
	if c.Length > 0 {
		length = fmt.Sprintf("%d:%02d", c.Length/60, c.Length%60)
	}
	fmt.Fprintf(w, "       %s | %s | %s | %.1f MB\n", c.Username, c.Path, length, float64(c.Size)/(1<<20))
	fmt.Fprint(w, c.Explain("         "))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
