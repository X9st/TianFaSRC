package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandRejectsImageFlagInjection(t *testing.T) {
	command := Command{Workspace: Workspace{Root: t.TempDir()}, Image: "--privileged"}
	_, err := command.Execute(context.Background(), "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002", json.RawMessage(`{"args":["id"]}`))
	if err == nil || !strings.Contains(err.Error(), "镜像") {
		t.Fatalf("unsafe image accepted: %v", err)
	}
}

func TestLimitedBufferCapsOutput(t *testing.T) {
	var output limitedBuffer
	input := bytes.Repeat([]byte("x"), 5<<20)
	count, err := output.Write(input)
	if err != nil || count != len(input) || len(output.Bytes()) != 4<<20 || !output.Truncated() {
		t.Fatalf("output cap: count=%d bytes=%d truncated=%v err=%v", count, len(output.Bytes()), output.Truncated(), err)
	}
}
