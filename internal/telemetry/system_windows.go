//go:build windows

package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// The command is fixed, uses Windows' own CIM provider, and accepts no remote text.
const inventoryScript = `[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$ErrorActionPreference='Stop'
$os=Get-CimInstance Win32_OperatingSystem | Select-Object -First 1
$cpu=Get-CimInstance Win32_Processor | Select-Object -First 1
$video=@(Get-CimInstance Win32_VideoController | Select-Object -First 8)
$gpus=@($video | ForEach-Object { @{name=[string]$_.Name;driver=[string]$_.DriverVersion} })
$displays=@($video | Where-Object { $_.CurrentHorizontalResolution -and $_.CurrentVerticalResolution } | ForEach-Object { @{name=[string]$_.Name;width=[uint32]$_.CurrentHorizontalResolution;height=[uint32]$_.CurrentVerticalResolution;refresh_hz=[uint32]$_.CurrentRefreshRate} })
@{os_version=([string]$os.Caption+' '+[string]$os.Version);cpu_model=[string]$cpu.Name;gpus=$gpus;displays=$displays} | ConvertTo-Json -Depth 5 -Compress`

type limitedOutput struct{ data []byte }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 64*1024 {
		return 0, errors.New("inventory output limit")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func platformInspect(parent context.Context, s *System) {
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	root := os.Getenv("SystemRoot")
	if !filepath.IsAbs(root) {
		return
	}
	exe := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.CommandContext(ctx, exe, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", inventoryScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var out limitedOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return
	}
	var detail struct {
		OSVersion string    `json:"os_version"`
		CPUModel  string    `json:"cpu_model"`
		GPUs      []GPU     `json:"gpus"`
		Displays  []Display `json:"displays"`
	}
	if json.Unmarshal(out.data, &detail) != nil {
		return
	}
	s.OSVersion = detail.OSVersion
	s.CPUModel = detail.CPUModel
	s.GPUs = detail.GPUs
	s.Displays = []Display{}
	for _, d := range detail.Displays {
		if d.Width > 32768 || d.Height > 32768 {
			continue
		}
		if d.Refresh > 2000 {
			d.Refresh = 0
		}
		s.Displays = append(s.Displays, d)
	}
	s.HardwareProbe = "ok"
}
