package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Workspace struct {
	Root string
}

func (w Workspace) taskDir(taskID, intentID string) (string, error) {
	if w.Root == "" || !uuidLike(taskID) || !uuidLike(intentID) {
		return "", errors.New("工作区配置或身份无效")
	}
	dir := filepath.Join(w.Root, "workspaces", taskID, intentID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

func uuidLike(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, char := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if char != '-' {
				return false
			}
		} else if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func safeName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 180 && !strings.HasPrefix(name, ".") && !strings.ContainsAny(name, "/\\\x00")
}

type fileInput struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (w Workspace) Execute(ctx context.Context, taskID, intentID, name string, raw json.RawMessage) (Result, error) {
	dir, err := w.taskDir(taskID, intentID)
	if err != nil {
		return Result{}, err
	}
	if name == "workspace.list" {
		var input struct{}
		if err := decodeStrict(raw, &input); err != nil {
			return Result{}, err
		}
		items, err := os.ReadDir(dir)
		if err != nil {
			return Result{}, err
		}
		var names []string
		for _, item := range items {
			if item.Type().IsRegular() && safeName(item.Name()) {
				names = append(names, item.Name())
			}
		}
		data, _ := json.Marshal(names)
		return Result{Summary: fmt.Sprintf("工作区包含 %d 个文件", len(names)), Output: data, ContentType: "application/json"}, nil
	}
	var input fileInput
	if err := decodeStrict(raw, &input); err != nil || !safeName(input.Name) {
		return Result{}, errors.New("文件名或输入无效")
	}
	file := filepath.Join(dir, input.Name)
	if info, statErr := os.Lstat(file); statErr == nil && !info.Mode().IsRegular() {
		return Result{}, errors.New("只允许普通文件")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return Result{}, statErr
	}
	if name == "workspace.read" {
		if input.Content != "" {
			return Result{}, errors.New("读取操作不接受 content")
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return Result{}, err
		}
		if len(data) > 4<<20 {
			return Result{}, errors.New("文件超过 4 MiB 上限")
		}
		return Result{Summary: fmt.Sprintf("读取 %s（%d 字节）", input.Name, len(data)), Output: data, ContentType: "text/plain"}, nil
	}
	if name != "workspace.write" || len(input.Content) > 1<<20 {
		return Result{}, errors.New("写入内容超过上限或工具名无效")
	}
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}
	temp, err := os.CreateTemp(dir, ".pending-*")
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(temp.Name())
	if err = temp.Chmod(0600); err == nil {
		_, err = io.WriteString(temp, input.Content)
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Result{}, err
	}
	if err = os.Rename(temp.Name(), file); err != nil {
		return Result{}, err
	}
	return Result{Summary: fmt.Sprintf("写入 %s（%d 字节）", input.Name, len(input.Content)), Output: []byte(input.Name), ContentType: "text/plain"}, nil
}

type Command struct {
	Workspace Workspace
	Image     string
}

var imageName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@:-]{0,239}$`)

type commandInput struct {
	Args []string `json:"args"`
}

// Commands run in an isolated, networkless container. There is no host shell,
// host environment, Docker socket mount or network namespace access.
func (c Command) Execute(ctx context.Context, taskID, intentID string, raw json.RawMessage) (Result, error) {
	var input commandInput
	if err := decodeStrict(raw, &input); err != nil || len(input.Args) == 0 || len(input.Args) > 32 {
		return Result{}, errors.New("命令必须提供参数数组")
	}
	for _, arg := range input.Args {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return Result{}, errors.New("命令参数无效")
		}
	}
	if !imageName.MatchString(c.Image) {
		return Result{}, errors.New("未配置合法的隔离命令镜像")
	}
	if os.Getuid() == 0 {
		return Result{}, errors.New("命令执行器不能以 root 身份启动")
	}
	dir, err := c.Workspace.taskDir(taskID, intentID)
	if err != nil {
		return Result{}, err
	}
	commandCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "64", "--cpus", "1", "--memory", "256m", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m", "--mount", "type=bind,src=" + dir + ",dst=/workspace", "--workdir", "/workspace", c.Image}
	args = append(args, input.Args...)
	cmd := exec.CommandContext(commandCtx, "docker", args...)
	var captured limitedBuffer
	cmd.Stdout, cmd.Stderr = &captured, &captured
	err = cmd.Run()
	output := captured.Bytes()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else {
			return Result{}, err
		}
	}
	return Result{Summary: fmt.Sprintf("隔离命令退出码 %d（捕获 %d 字节）", code, len(output)), Output: output, ExitCode: code, ContentType: "text/plain", Truncated: captured.Truncated()}, nil
}

type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	length := len(data)
	remaining := (4 << 20) - b.buf.Len()
	if remaining < length {
		b.truncated = true
		if remaining < 0 {
			remaining = 0
		}
	}
	_, _ = b.buf.Write(data[:min(length, remaining)])
	return length, nil
}

func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte{}, b.buf.Bytes()...)
}

func (b *limitedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}
