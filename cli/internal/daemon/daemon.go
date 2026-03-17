package daemon

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Info stores daemon metadata persisted to disk.
type Info struct {
	PID         int    `json:"pid"`
	ProjectPath string `json:"project_path"`
}

// Status represents the current daemon state.
type Status struct {
	Running     bool
	PID         int
	ProjectPath string
	LogFile     string
}

// daemonsDir returns ~/.cix/daemons/, creating it if needed.
func daemonsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".cix", "daemons")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// logsDir returns ~/.cix/logs/, creating it if needed.
func logsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".cix", "logs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// infoFile returns the path to the daemon info file for a project.
func infoFile(projectPath string) (string, error) {
	dir, err := daemonsDir()
	if err != nil {
		return "", err
	}
	hash := fmt.Sprintf("%x", md5.Sum([]byte(projectPath)))
	return filepath.Join(dir, hash+".json"), nil
}

// LogFilePath returns the log file path for a project's watcher daemon.
func LogFilePath(projectPath string) (string, error) {
	dir, err := logsDir()
	if err != nil {
		return "", err
	}
	hash := fmt.Sprintf("%x", md5.Sum([]byte(projectPath)))
	return filepath.Join(dir, "watcher-"+hash+".log"), nil
}

// Start launches the watcher as a background daemon for the given project.
func Start(projectPath string) (int, error) {
	// Check if already running for this project
	if status := GetStatus(projectPath); status.Running {
		return 0, fmt.Errorf("watcher already running for %s (PID %d)", projectPath, status.PID)
	}

	logFile, err := LogFilePath(projectPath)
	if err != nil {
		return 0, fmt.Errorf("get log path: %w", err)
	}

	lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return 0, fmt.Errorf("open log file: %w", err)
	}

	exe, err := os.Executable()
	if err != nil {
		lf.Close()
		return 0, fmt.Errorf("get executable: %w", err)
	}

	cmd := exec.Command(exe, "watch", "--daemon-mode", projectPath)
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	if err := cmd.Start(); err != nil {
		lf.Close()
		return 0, fmt.Errorf("start daemon: %w", err)
	}

	pid := cmd.Process.Pid

	if err := writeInfo(projectPath, pid); err != nil {
		cmd.Process.Kill()
		lf.Close()
		return 0, fmt.Errorf("write daemon info: %w", err)
	}

	cmd.Process.Release()
	lf.Close()

	return pid, nil
}

// Stop terminates the daemon for the given project.
func Stop(projectPath string) error {
	status := GetStatus(projectPath)
	if !status.Running {
		return fmt.Errorf("no watcher running for %s", projectPath)
	}

	process, err := os.FindProcess(status.PID)
	if err != nil {
		removeInfo(projectPath)
		return fmt.Errorf("find process: %w", err)
	}

	if err := process.Signal(syscall.SIGTERM); err != nil {
		removeInfo(projectPath)
		return fmt.Errorf("send signal: %w", err)
	}

	removeInfo(projectPath)
	return nil
}

// StopAll terminates all running watcher daemons.
func StopAll() (int, error) {
	all := ListAll()
	stopped := 0
	for _, s := range all {
		if s.Running {
			if err := Stop(s.ProjectPath); err == nil {
				stopped++
			}
		}
	}
	return stopped, nil
}

// GetStatus checks if a daemon is running for the given project.
func GetStatus(projectPath string) Status {
	file, err := infoFile(projectPath)
	if err != nil {
		return Status{}
	}

	logFile, _ := LogFilePath(projectPath)

	info, err := readInfo(file)
	if err != nil {
		return Status{LogFile: logFile}
	}

	if isProcessRunning(info.PID) {
		return Status{
			Running:     true,
			PID:         info.PID,
			ProjectPath: info.ProjectPath,
			LogFile:     logFile,
		}
	}

	// Stale — process died, clean up
	os.Remove(file)
	return Status{ProjectPath: projectPath, LogFile: logFile}
}

// ListAll returns the status of all known daemons.
func ListAll() []Status {
	dir, err := daemonsDir()
	if err != nil {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var result []Status
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		file := filepath.Join(dir, entry.Name())
		info, err := readInfo(file)
		if err != nil {
			os.Remove(file)
			continue
		}

		logFile, _ := LogFilePath(info.ProjectPath)

		if isProcessRunning(info.PID) {
			result = append(result, Status{
				Running:     true,
				PID:         info.PID,
				ProjectPath: info.ProjectPath,
				LogFile:     logFile,
			})
		} else {
			// Stale, clean up
			os.Remove(file)
		}
	}

	return result
}

func writeInfo(projectPath string, pid int) error {
	file, err := infoFile(projectPath)
	if err != nil {
		return err
	}
	data, err := json.Marshal(Info{PID: pid, ProjectPath: projectPath})
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0644)
}

func readInfo(file string) (*Info, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func removeInfo(projectPath string) {
	file, err := infoFile(projectPath)
	if err == nil {
		os.Remove(file)
	}
}

func isProcessRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
