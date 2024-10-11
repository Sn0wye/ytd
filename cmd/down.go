package cmd

import (
	"context"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Sn0wye/ytd/pkg/downloader"
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
	DEFAULT_DOWNLOAD_DIR string
)

func init() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	DEFAULT_DOWNLOAD_DIR = filepath.Join(homeDir, "Downloads")

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
	videoTemplates := &promptui.SelectTemplates{
		Label: fmt.Sprintf(`
Video Information
Title:    %s
Author:   %s
Duration: %s

Available Video Formats:
Size (MB)    | Quality`,
			video.Title, video.Author, video.Duration),
		Active:   `▶ {{ .ContentLength | formatSize | printf "%-10s" }} | {{ .QualityLabel | printf "%-15s" }}`,
		Inactive: `  {{ .ContentLength | formatSize | printf "%-10s" }} | {{ .QualityLabel | printf "%-15s" }}`,
		Selected: `✔ {{ .ContentLength | formatSize | printf "%-10s" }} | {{ .QualityLabel | printf "%-15s" }}`,
		Details: `
--------- Format Details ----------
Size:    {{ .ContentLength | formatSize }}
Quality: {{ .QualityLabel }}
FPS:     {{ .Fps }}
Type:    {{ .MimeType }}`,
	}

	// Define templates for audio format selection
	audioTemplates := &promptui.SelectTemplates{
		Label: fmt.Sprintf(`
Available Audio Formats:
Size (MB)    | Bitrate (Kbps)`),
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

	// Set up output paths
	outputVideoFile := filepath.Join(DEFAULT_DOWNLOAD_DIR, "video.mp4")
	outputAudioFile := filepath.Join(DEFAULT_DOWNLOAD_DIR, "audio.m4a")
	finalOutputFile := filepath.Join(DEFAULT_DOWNLOAD_DIR, "final_output.mp4")

	selectedVideoFormat := videoFormats[videoIndex]
	selectedAudioFormat := audioFormats[audioIndex]

	// Initialize downloader
	down := downloader.Downloader{
		Client:    client,
		OutputDir: DEFAULT_DOWNLOAD_DIR,
	}

	// Download video
	fmt.Println("Downloading video...")
	err = down.DownloadVideo(context.Background(), outputVideoFile, video, selectedVideoFormat.ItagNo)
	if err != nil {
		return err
	}

	// Download audio
	fmt.Println("Downloading audio...")
	err = down.DownloadAudio(context.Background(), outputAudioFile, video, selectedAudioFormat.ItagNo)
	if err != nil {
		return err
	}

	// Merge video and audio using ffmpeg
	fmt.Println("Merging video and audio...")
	err = mergeVideoAudio(outputVideoFile, outputAudioFile, finalOutputFile)
	if err != nil {
		return err
	}

	fmt.Printf("\nVideo and audio downloaded and merged successfully: %s\n", finalOutputFile)
	return nil
}

// mergeVideoAudio uses ffmpeg to merge video and audio into a single file
func mergeVideoAudio(videoFile, audioFile, outputFile string) error {
	cmd := exec.Command("ffmpeg", "-i", videoFile, "-i", audioFile, "-c:v", "copy", "-c:a", "aac", outputFile)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
