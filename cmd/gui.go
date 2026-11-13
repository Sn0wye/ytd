package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Sn0wye/ytd/pkg/gui"
	"github.com/spf13/cobra"
)

var (
	guiPort   int
	guiNoOpen bool
)

var guiCmd = &cobra.Command{
	Use:   "gui",
	Short: "Open the graphical interface in your browser",
	Long: `Start ytd's graphical interface: a page served by ytd itself on this
computer (127.0.0.1 only) and opened in your default browser. Paste a video
or playlist link, pick the format and quality, and follow the downloads.

ytd stops when you press Ctrl+C, click Quit on the page, or about 30 seconds
after the last ytd tab is closed with nothing left to download.

On Windows, double-clicking ytd.exe opens the graphical interface too.`,
	Args: cobra.NoArgs,
	Run: func(_ *cobra.Command, _ []string) {
		if err := runGUI(guiPort, !guiNoOpen); err != nil {
			fmt.Fprintln(os.Stderr, "ytd gui:", err)
			os.Exit(1)
		}
	},
}

func init() {
	guiCmd.Flags().IntVar(&guiPort, "port", 0, "port to listen on (default: a free port)")
	guiCmd.Flags().BoolVar(&guiNoOpen, "no-open", false, "print the address instead of opening the browser")
	rootCmd.AddCommand(guiCmd)
}

func runGUI(port int, open bool) error {
	out := "."
	if home, err := os.UserHomeDir(); err == nil {
		out = filepath.Join(home, "Downloads")
	}
	s := gui.New(gui.Config{
		Version:     version,
		OutputDir:   out,
		TmpDir:      os.TempDir(),
		Connections: 8,
		IdleTimeout: 30 * time.Second,
	})
	ln, url, err := s.Listen(port)
	if err != nil {
		return err
	}

	fmt.Println("ytd is running at", url)
	if open {
		if err := gui.OpenBrowser(url); err != nil {
			fmt.Println("Could not open the browser; open the address above.")
		}
	}
	fmt.Println("Keep this window open while you use ytd. Press Ctrl+C (or click Quit on the page) to stop.")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return s.Serve(ctx, ln)
}
