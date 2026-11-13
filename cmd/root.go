package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "ytd",
	Short: "YTD - A youtube video/audio downloader",
	Long: `YTD downloads YouTube videos and audio, letting you pick the video and audio quality.
It needs no other programs and no login; ffmpeg is only used for mp3 output and --h264.
Run "ytd gui" for the graphical interface.`,
	Run: func(cmd *cobra.Command, _ []string) {
		// Double-clicking ytd.exe gives a console that closes as soon as the
		// help is printed; open the graphical interface instead.
		if startedByDoubleClick() {
			if err := runGUI(0, true); err != nil {
				fmt.Fprintln(os.Stderr, "ytd gui:", err)
				fmt.Println("Press Enter to close.")
				fmt.Scanln()
				os.Exit(1)
			}
			return
		}
		cmd.Help()
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}
