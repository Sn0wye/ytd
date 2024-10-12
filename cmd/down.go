package cmd

import (
	"context"
	"fmt"
	"html/template"
	"os"
	"path/filepath"

	"github.com/Sn0wye/ytd/pkg/downloader"
	"github.com/Sn0wye/ytd/pkg/ffmpeg"
	"github.com/Sn0wye/ytd/pkg/utils"
	"github.com/kkdai/youtube/v2"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

// downCmd represents the down command
var downCmd = &cobra.Command{
	Use:   "down",
	Short: "Download a YouTube video with highest quality video and audio.",
	Long:  `This command allows you to download a YouTube video with both video and audio streams at the highest available quality.`,
	Run: func(_ *cobra.Command, args []string) {
		utils.ExitOnError(download(args[0]))
	},
}

var (
	TMP_DIR            string
	OUTPUT_DIR         string
	DEFAULT_OUTPUT_DIR string
	OUTPUT_FILE_NAME   string
)

func init() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	TMP_DIR = filepath.Join(homeDir, "tmp")
	DEFAULT_OUTPUT_DIR = filepath.Join(homeDir, "Downloads")

	downCmd.Flags().StringVarP(&OUTPUT_FILE_NAME, "filename", "f", "", "Output filename for downloaded video")
	downCmd.Flags().StringVarP(&OUTPUT_DIR, "output-dir", "o", DEFAULT_OUTPUT_DIR, "Output directory for downloaded video")
	downCmd.Flags().StringVarP(&TMP_DIR, "tmp-dir", "t", TMP_DIR, "Temporary directory for video and audio files")
	rootCmd.AddCommand(downCmd)
}

func download(videoURL string) error {
	videoID, err := utils.GetVideoID(videoURL)
	if err != nil {
		return err
	}

	client := youtube.Client{}

	video, err := client.GetVideo(videoID)
	if err != nil {
		return err
	}

	// Filter video and audio formats
	videoFormats := video.Formats.Type("video")
	audioFormats := video.Formats.Type("audio")

	// Define templates for video format selection
	// Define templates for video format selection
	videoTemplates := &promptui.SelectTemplates{
		Label: fmt.Sprintf(`
Video Information
Title:    %s
Author:   %s
Duration: %s

Available Video Formats:
Size (MB)    | Quality
`,
			video.Title, video.Author, video.Duration),
		Active:   `▶ {{ .ContentLength | formatSize | printf "%-10s" }} | {{ .QualityLabel | printf "%-15s" }}`,
		Inactive: `  {{ .ContentLength | formatSize | printf "%-10s" }} | {{ .QualityLabel | printf "%-15s" }}`,
		Selected: `✔ {{ .ContentLength | formatSize | printf "%-10s" }} | {{ .QualityLabel | printf "%-15s" }}`,
		Details: `
--------- Format Details ----------
Size:    {{ .ContentLength | formatSize }}
Quality: {{ .QualityLabel }}
FPS:     {{ .FPS }}
Type:    {{ .MimeType }}`,
	}

	// Define templates for audio format selection
	audioTemplates := &promptui.SelectTemplates{
		Label: `
Available Audio Formats:
Size (MB)    | Bitrate (Kbps)`,
		Active:   `▶ {{ .ContentLength | formatSize | printf "%-10s" }} | {{ .Bitrate | printf "%-15d" }}`,
		Inactive: `  {{ .ContentLength | formatSize | printf "%-10s" }} | {{ .Bitrate | printf "%-15d" }}`,
		Selected: `✔ {{ .ContentLength | formatSize | printf "%-10s" }} | {{ .Bitrate | printf "%-15d" }}`,
		Details: `
--------- Audio Format Details ----------
Size:    {{ .ContentLength | formatSize }}
Bitrate: {{ .Bitrate }}
Type:    {{ .MimeType }}`,
	}

	// Define the funcMap for formatting sizes
	funcMap := template.FuncMap{
		"formatSize": func(size int64) string {
			return fmt.Sprintf("%.2f MB", float64(size)/1048576)
		},
		"faint": func(s string) string {
			return fmt.Sprintf("\033[2m%s\033[0m", s)
		},
	}

	// Apply the funcMap to both video and audio templates
	videoTemplates.FuncMap = funcMap
	audioTemplates.FuncMap = funcMap

	// Prompt UI for video format selection
	videoPrompt := promptui.Select{
		Label:     "Select a video format to download",
		Items:     videoFormats,
		Templates: videoTemplates,
		Size:      20,
		HideHelp:  true,
	}

	videoIndex, _, err := videoPrompt.Run()

	if err != nil {
		fmt.Printf("Video format selection failed: %v\n", err)
		return err
	}

	// Prompt UI for audio format selection
	audioPrompt := promptui.Select{
		Label:     "Select an audio format to download",
		Items:     audioFormats,
		Templates: audioTemplates,
		Size:      10,
		HideHelp:  true,
	}

	audioIndex, _, err := audioPrompt.Run()

	if err != nil {
		fmt.Printf("Audio format selection failed: %v\n", err)
		return err
	}

	if OUTPUT_FILE_NAME == "" {
		OUTPUT_FILE_NAME = downloader.SlugifyFilename(video.Title) + ".mp4"
	}

	// Set up output paths
	outputVideoFile := filepath.Join(TMP_DIR, "video.mp4")
	outputAudioFile := filepath.Join(TMP_DIR, "audio.m4a")
	finalOutputFile := filepath.Join(OUTPUT_DIR, OUTPUT_FILE_NAME)

	selectedVideoFormat := videoFormats[videoIndex]
	selectedAudioFormat := audioFormats[audioIndex]

	// Initialize downloader
	down := downloader.Downloader{
		Client:    client,
		OutputDir: OUTPUT_DIR,
	}

	err = down.DownloadVideo(context.Background(), outputVideoFile, video, selectedVideoFormat.ItagNo)
	if err != nil {
		return err
	}

	err = down.DownloadAudio(context.Background(), outputAudioFile, video, selectedAudioFormat.ItagNo)
	if err != nil {
		return err
	}

	// Merge video and audio using ffmpeg
	fmt.Println("Merging video and audio...")
	err = ffmpeg.MergeVideoWithAudio(outputVideoFile, outputAudioFile, finalOutputFile)
	if err != nil {
		return err
	}

	// Clean up tmp video file
	err = os.Remove(outputVideoFile)

	if err != nil {
		return err
	}

	// Clean up tmp audio file
	err = os.Remove(outputAudioFile)

	if err != nil {
		return err
	}

	fmt.Printf("\nVideo and audio downloaded and merged successfully: %s\n", finalOutputFile)
	return nil
}
