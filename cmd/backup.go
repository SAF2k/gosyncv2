package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-co-op/gocron"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
)

var (
	sourceDir      string
	backupDir      string
	schedulePeriod time.Duration
	includePaths   []string
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
	Example: `  gorsync backup --source=/home/user/data --destination=/mnt/backup --interval=1h
  gorsync backup -s /path/to/source -d /path/to/destination
  gorsync backup -s /path/to/source -d /path/to/destination -i 30m --include=filename1.txt,folder1`,
}

func init() {
	rootCmd.AddCommand(backupCmd)
	backupCmd.Flags().StringVarP(&sourceDir, "source", "s", "", "Source directory (required)")
	backupCmd.Flags().StringVarP(&backupDir, "destination", "d", "", "Backup directory (required)")
	backupCmd.Flags().DurationVarP(&schedulePeriod, "interval", "i", 0, "Schedule interval for backups (e.g., 1h, 30m). If omitted, real-time sync will be used.")
	backupCmd.Flags().StringArrayVarP(&includePaths, "include", "c", nil, "Comma-separated list of file or folder names to sync (optional)")

	backupCmd.MarkFlagRequired("source")
	backupCmd.MarkFlagRequired("destination")
}

func runScheduledBackup() {
	fmt.Printf("Starting scheduled backup at %s\n", time.Now().Format("2006-01-02 15:04:05"))
	err := syncDirectories(sourceDir, backupDir, includePaths)
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

	var pathsToWatch []string
	if len(includePaths) > 0 {
		for _, includePath := range includePaths {
			absPath, err := filepath.Abs(filepath.Join(sourceDir, includePath))
			if err != nil {
				fmt.Println("Error getting absolute path:", err)
				return
			}
			pathsToWatch = append(pathsToWatch, absPath)
		}
	} else {
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
	}

	done := make(chan bool)
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create {
					if shouldWatch(event.Name, includePaths) {
						fmt.Println("Detected modification:", event.Name)
						relPath, err := filepath.Rel(sourceDir, event.Name)
						if err != nil {
							fmt.Println("Error getting relative path:", err)
							continue
						}
						destPath := filepath.Join(backupDir, relPath)
						copyFileWithProgress(event.Name, destPath)
					}
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

func syncDirectories(src, dst string, includes []string) error {
	includeMap := make(map[string]bool)
	for _, includePath := range includes {
		includeMap[includePath] = true
	}

	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if len(includeMap) > 0 && !matchesAnyInclude(path, includes) {
			return nil
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
			if shouldCopyFile(path, destPath, info) {
				err := copyFileWithProgress(path, destPath)
				if err != nil {
					return err
				}
			}
		}

		return nil
	})
}

func matchesAnyInclude(path string, includes []string) bool {
	for _, include := range includes {
		if strings.Contains(filepath.Base(path), include) || strings.Contains(filepath.Dir(path), include) {
			return true
		}
	}
	return false
}

func shouldWatch(path string, includes []string) bool {
	if len(includes) == 0 {
		return true
	}
	for _, include := range includes {
		if strings.Contains(filepath.Base(path), include) || strings.Contains(filepath.Dir(path), include) {
			return true
		}
	}
	return false
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
	formattedElapsedTime := fmt.Sprintf("%.2f seconds", elapsedTime.Seconds())

	// Move cursor to the beginning of the line and print the final progress
	fmt.Printf("\rCopying %s 100%% |%s| (%s) %s\n", filepath.Base(src), bar.String(), formatBytesPerSecond(srcInfo.Size(), elapsedTime), formattedElapsedTime)

	// Ensure file permissions are copied
	err = dstFile.Sync()
	if err != nil {
		return err
	}

	return os.Chmod(dst, srcInfo.Mode())
}

func formatBytesPerSecond(size int64, elapsed time.Duration) string {
	if elapsed.Seconds() == 0 {
		return "0 MB/s"
	}
	mbps := float64(size) / (1024 * 1024) / elapsed.Seconds()
	return fmt.Sprintf("%.2f MB/s", mbps)
}
