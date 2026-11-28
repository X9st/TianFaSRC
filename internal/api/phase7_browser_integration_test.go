package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func browserContext(t *testing.T) context.Context {
	t.Helper()
	if os.Getenv("TIANFA_BROWSER_E2E") != "1" {
		t.Skip("set TIANFA_BROWSER_E2E=1 to run real Chrome acceptance")
	}
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	if binary := os.Getenv("TIANFA_CHROME_PATH"); binary != "" {
		options = append(options, chromedp.ExecPath(binary))
	}
	options = append(options, chromedp.Headless, chromedp.NoSandbox)
	allocator, cancelAllocator := chromedp.NewExecAllocator(context.Background(), options...)
	t.Cleanup(cancelAllocator)
	browser, cancelBrowser := chromedp.NewContext(allocator, chromedp.WithErrorf(t.Logf))
	t.Cleanup(cancelBrowser)
	ctx, cancelTimeout := context.WithTimeout(browser, 180*time.Second)
	t.Cleanup(cancelTimeout)
	return ctx
}

func browserClickButton(ctx context.Context, label string) error {
	script := fmt.Sprintf(`(() => { const b = [...document.querySelectorAll('button')].find(x => x.textContent?.includes(%q) && !x.disabled); if (!b) return false; b.click(); return true; })()`, label)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](script))
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("button %q not found", label)
}

func browserSelect(ctx context.Context, selector, value string) error {
	script := fmt.Sprintf(`(() => { const e = document.querySelector(%q); if (!e) return false; e.value = %q; e.dispatchEvent(new Event('change', {bubbles:true})); return true; })()`, selector, value)
	ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](script))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("select %q not found", selector)
	}
	return nil
}

func browserHasText(ctx context.Context, value string) (bool, error) {
	return chromedp.Run(ctx, chromedp.Evaluate[bool](fmt.Sprintf(`document.body?.innerText.includes(%q) || false`, value)))
}

func browserWaitText(ctx context.Context, value string) error {
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		ok, err := browserHasText(ctx, value)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("browser text %q did not appear", value)
}

func browserWaitCount(ctx context.Context, selector string, want int) error {
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		got, err := chromedp.Run(ctx, chromedp.Evaluate[int](fmt.Sprintf(`document.querySelectorAll(%q).length`, selector)))
		if err != nil {
			return err
		}
		if got == want {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("browser selector %q did not reach count %d", selector, want)
}

func TestPhase7BrowserSmoke(t *testing.T) {
	ctx := browserContext(t)
	handler := NewHandler(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	server := httptest.NewServer(handler)
	defer server.Close()
	if err := chromedp.Do(ctx, chromedp.Navigate(server.URL+"/login/")); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if err := chromedp.Do(ctx, chromedp.WaitVisible(chromedp.CSS("h1"))); err != nil {
		t.Fatal(err)
	}
	heading, err := chromedp.Run(ctx, chromedp.Text(chromedp.CSS("h1")))
	if err != nil {
		t.Fatal(err)
	}
	if heading == "" {
		t.Fatal("login page did not render")
	}
}
