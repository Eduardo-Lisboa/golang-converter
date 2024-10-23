package converter

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

type VideoConverter struct {
	db *sql.DB
}

type VideoTask struct {
	VideoID int    `json:"video_id"`
	Path    string `json:"path"`
}

func NewVideoConverter(db *sql.DB) *VideoConverter {
	return &VideoConverter{
		db: db,
	}
}

func (vc *VideoConverter) Handle(msg []byte) {
	var task VideoTask
	if err := json.Unmarshal(msg, &task); err != nil {
		vc.logError(task, "Error decoding task", err)
		return
	}

	if IsProcessed(vc.db, task.VideoID) {
		slog.Warn("Video already processed", slog.Int("video_id", task.VideoID))
		return
	}

	if err := vc.processVideo(&task); err != nil {
		vc.logError(task, "Error processing video", err)
		return
	}

	if err := MarkProcessed(vc.db, task.VideoID); err != nil {
		vc.logError(task, "Error marking video as processed", err)
		return
	}

	slog.Info("Video processing completed", slog.Int("video_id", task.VideoID))
}

func (vc *VideoConverter) processVideo(task *VideoTask) error {
	mergedFile := filepath.Join(task.Path, "merged.mp4")
	mpegDashPath := filepath.Join(task.Path, "mpeg-dash")

	slog.Info("Merging", slog.String("path", task.Path))

	err := vc.mergeChunks(task.Path, mergedFile)
	if err != nil {
		vc.logError(*task, "Error merging chunks", err)
		return err
	}

	slog.Info("Creating mpeg-dash dir", slog.String("path", task.Path))
	err = os.MkdirAll(mpegDashPath, os.ModePerm)
	if err != nil {
		vc.logError(*task, "Error creating mpeg-dash directory", err)
		return err
	}

	slog.Info("Converting video to mpeg-dash", slog.String("path", task.Path))
	ffmpegCmd := exec.Command(
		"ffmpeg", "-i", mergedFile, // Arquivo de entrada
		"-f", "dash", // Formato de saída
		filepath.Join(mpegDashPath, "output.mpd"), // Caminho para salvar o arquivo .mpd
	)

	output, err := ffmpegCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to convert to MPEG-DASH: %v, output: %s", err, string(output))
	}
	slog.Info("Video processing completed", slog.String("path", mpegDashPath))

	slog.Info("Removing merged file", slog.String("path", mergedFile))
	if err := os.Remove(mergedFile); err != nil {
		slog.Warn("Failed to remove merged file", slog.String("file", mergedFile), slog.String("error", err.Error()))
	}

	return nil
} /*  */

func (vc *VideoConverter) logError(task VideoTask, message string, err error) {

	errorData := map[string]any{
		"video_id": task.VideoID,
		"error":    message,
		"details":  err.Error(),
		"time":     time.Now(),
	}

	serialized, _ := json.Marshal(errorData)
	slog.Error("Processing error", slog.String("error_detalis", string(serialized)))

	RegisterError(vc.db, errorData, err)

}

func (vc *VideoConverter) extractNumber(fileName string) int {

	re := regexp.MustCompile(`\d+`)
	numStr := re.FindString(filepath.Base(fileName))
	num, err := strconv.Atoi(numStr)
	if err != nil {
		return -1
	}

	return num
}

func (vc *VideoConverter) mergeChunks(inputDir, outputFile string) error {
	chunks, err := filepath.Glob(filepath.Join(inputDir, "*.chunk"))
	if err != nil {
		return fmt.Errorf("Error reading chunks: %v", err)
	}

	sort.Slice(chunks, func(i, j int) bool {
		return vc.extractNumber(chunks[i]) < vc.extractNumber(chunks[j])
	})

	outputDir := filepath.Dir(outputFile)
	err = os.MkdirAll(outputDir, os.ModePerm)
	if err != nil {
		return fmt.Errorf("Error creating output directory: %v", err)
	}

	output, err := os.Create(outputFile)
	if err != nil {
		return fmt.Errorf("Error creating output file: %v", err)
	}
	defer output.Close()

	for _, chunk := range chunks {
		input, err := os.Open(chunk)
		if err != nil {
			return fmt.Errorf("Error opening chunk %s: %v", chunk, err)
		}
		defer input.Close()

		_, err = io.Copy(output, input)
		if err != nil {
			return fmt.Errorf("Error copying chunk %s to output: %v", chunk, err)
		}
	}

	return nil
}
