package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-co-op/gocron"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
)

var (
	sourceDir              string
	backupDir              string
	schedulePeriod         time.Duration
	includePatterns        []string
	maxConcurrentTransfers int
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Start the backup and synchronization process",
	Long: `Start the backup and synchronization process from a source to a destination directory.
Supports both real-time file watching and scheduled sync operations.`,
	Run: func(cmd *cobra.Command, args []string) {
		if schedulePeriod > 0 {
			schedule := gocron.NewScheduler(time.Local)
			schedule.Every(schedulePeriod).Do(runScheduledBackup)
			schedule.StartBlocking()
		} else {
			runRealTimeSync()
		}
	},
	Example: `  gorsync backup --source=/home/user/data --destination=/mnt/backup --interval=1h --include="*.txt,backup*" --max-transfers=5
  gorsync backup -s /path/to/source -d /path/to/destination -i 30m --include="*.jpg,*.png" --max-transfers=3`,
}

func init() {
	rootCmd.AddCommand(backupCmd)
	backupCmd.Flags().StringVarP(&sourceDir, "source", "s", "", "Source directory (required)")
	backupCmd.Flags().StringVarP(&backupDir, "destination", "d", "", "Backup directory (required)")
	backupCmd.Flags().DurationVarP(&schedulePeriod, "interval", "i", 0, "Schedule interval for backups (e.g., 1h, 30m). If omitted, real-time sync will be used.")
	backupCmd.Flags().StringSliceVarP(&includePatterns, "include", "e", nil, "Comma-separated list of file or folder patterns to include for synchronization (e.g., *.jpg,backup*)")
	backupCmd.Flags().IntVarP(&maxConcurrentTransfers, "max-transfers", "m", 1, "Maximum number of concurrent file transfers (default is 1)")

	backupCmd.MarkFlagRequired("source")
	backupCmd.MarkFlagRequired("destination")
}

func runScheduledBackup() {
	fmt.Printf("Starting scheduled backup at %s\n", time.Now().Format("2006-01-02 15:04:05"))
	err := syncDirectories(sourceDir, backupDir)
	if err != nil {
		fmt.Printf("Error during scheduled backup: %v\n", err)
	} else {
		fmt.Printf("Scheduled backup completed at %s\n", time.Now().Format("2006-01-02 15:04:05"))
	}
}

func runRealTimeSync() {
	fmt.Printf("Starting real-time sync at %s\n", time.Now().Format("2006-01-02 15:04:05"))

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Println("Error creating watcher:", err)
		return
	}
	defer watcher.Close()

	err = filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			err = watcher.Add(path)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		fmt.Println("Error walking source directory:", err)
		return
	}

	done := make(chan bool)
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if (event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create) && shouldInclude(event.Name) {
					fmt.Println("Detected modification:", event.Name)
					relPath, err := filepath.Rel(sourceDir, event.Name)
					if err != nil {
						fmt.Println("Error getting relative path:", err)
						continue
					}
					destPath := filepath.Join(backupDir, relPath)
					copyFileWithProgress(event.Name, destPath)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				fmt.Println("Error:", err)
			}
		}
	}()

	<-done
}

func syncDirectories(src, dst string) error {
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentTransfers)

	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		destPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			if _, err := os.Stat(destPath); os.IsNotExist(err) {
				if err := os.MkdirAll(destPath, info.Mode()); err != nil {
					return err
				}
			}
		} else {
			if shouldCopyFile(path, destPath, info) && shouldInclude(path) {
				wg.Add(1)
				sem <- struct{}{}
				go func(src, dst string) {
					defer wg.Done()
					defer func() { <-sem }()
					err := copyFileWithProgress(src, dst)
					if err != nil {
						fmt.Println("Error copying file:", err)
					}
				}(path, destPath)
			}
		}

		return nil
	})
	wg.Wait()
	return nil
}

func shouldCopyFile(src, dst string, srcInfo os.FileInfo) bool {
	dstInfo, err := os.Stat(dst)
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	return srcInfo.ModTime().After(dstInfo.ModTime())
}

func shouldInclude(path string) bool {
	if len(includePatterns) == 0 {
		return true
	}
	for _, pattern := range includePatterns {
		if strings.Contains(filepath.Base(path), pattern) {
			return true
		}
	}
	return false
}

func copyFileWithProgress(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	// Create a new progress bar for each file transfer
	bar := progressbar.NewOptions64(
		srcInfo.Size(),
		progressbar.OptionSetDescription(fmt.Sprintf("Copying %s", filepath.Base(src))),
		progressbar.OptionSetWidth(20),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionThrottle(100*time.Millisecond), // Update interval of 0.1 seconds
		progressbar.OptionShowBytes(true),
	)

	// Initialize variables for time calculation
	startTime := time.Now()

	// Copy file and update progress
	_, err = io.Copy(io.MultiWriter(dstFile, bar), srcFile)
	if err != nil {
		return err
	}

	// Finalize the progress bar and print final time taken
	elapsedTime := time.Since(startTime)
	fmt.Printf("\nCopying %s 100%% |%s| (%.2f MB/s) %.2f seconds\n", filepath.Base(src), bar.String(), calculateMBPerSecond(srcInfo.Size(), elapsedTime), float64(elapsedTime.Seconds()))

	// Ensure file permissions are copied
	err = dstFile.Sync()
	if err != nil {
		return err
	}

	return os.Chmod(dst, srcInfo.Mode())
}

func calculateMBPerSecond(size int64, elapsed time.Duration) float64 {
	if elapsed.Seconds() == 0 {
		return 0
	}
	return float64(size) / (elapsed.Seconds() * 1024 * 1024)
}
