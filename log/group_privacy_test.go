package log

import (
	"bytes"
	"context"
	"github.com/kzs0/bedrock/privacy"
	"log/slog"
	"strings"
	"testing"
)

func TestSensitiveGroupDoesNotLeakValue(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(NewHandler(&HandlerOptions{Output: &out, Format: "json"}))
	logger.LogAttrs(context.Background(), slog.LevelInfo, "event", slog.Group("password", slog.String("value", "SECRET_GROUP_VALUE")))
	if strings.Contains(out.String(), "SECRET_GROUP_VALUE") {
		t.Fatalf("sensitive group leaked: %s", out.String())
	}
}

func TestGroupedErrorAndBuiltinNamesRemainPrivate(t *testing.T) {
	for _, key := range []string{"error", "exception.message", "error.stack", "msg", "level", "time"} {
		var out bytes.Buffer
		logger := slog.New(NewHandler(&HandlerOptions{Output: &out, Format: "json"}))
		group := "request"
		if key == "msg" || key == "level" || key == "time" {
			group = "password"
		}
		logger.LogAttrs(context.Background(), slog.LevelInfo, "event", slog.Group(group, slog.String(key, "SECRET_GROUP_VALUE")))
		if strings.Contains(out.String(), "SECRET_GROUP_VALUE") {
			t.Fatalf("%s leaked: %s", key, out.String())
		}
	}
}

func TestGroupedAllowlistPreservesSafeStructure(t *testing.T) {
	for _, allow := range [][]string{nil, {"request"}, {"request.method"}} {
		var out bytes.Buffer
		logger := slog.New(NewHandler(&HandlerOptions{Output: &out, Format: "json", Policy: &privacy.Policy{AttributeKeys: allow}}))
		logger.LogAttrs(context.Background(), slog.LevelInfo, "event", slog.Group("request", slog.String("method", "GET")), slog.Group("unrelated", slog.String("value", "DROPPED_VALUE")))
		if !strings.Contains(out.String(), `"request":{"method":"GET"}`) {
			t.Fatalf("safe group lost: %s", out.String())
		}
		if allow != nil && strings.Contains(out.String(), "DROPPED_VALUE") {
			t.Fatalf("allowlist ignored: %s", out.String())
		}
	}
}

func TestErrorGroupDetailsRemainPrivate(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(NewHandler(&HandlerOptions{Output: &out, Format: "json"}))
	logger.LogAttrs(context.Background(), slog.LevelInfo, "event", slog.Group("error", slog.String("detail", "SECRET_GROUP_VALUE")))
	if strings.Contains(out.String(), "SECRET_GROUP_VALUE") {
		t.Fatalf("error group leaked: %s", out.String())
	}
}

func TestErrorAndExceptionNamespacesRespectOptIn(t *testing.T) {
	for _, namespace := range []string{"error", "exception", "request.error", "request.exception"} {
		for _, details := range []bool{false, true} {
			var out bytes.Buffer
			logger := slog.New(NewHandler(&HandlerOptions{Output: &out, Format: "json", Policy: &privacy.Policy{AllowErrorDetails: details}}))
			logger.LogAttrs(context.Background(), slog.LevelInfo, "event", slog.Group(namespace, slog.String("detail", "ERROR_DETAIL_SENTINEL")))
			if strings.Contains(out.String(), "ERROR_DETAIL_SENTINEL") != details {
				t.Fatalf("namespace=%s details=%v output=%s", namespace, details, out.String())
			}
		}
	}
}

func TestNestedErrorAndSensitiveAncestorsStayPrivate(t *testing.T) {
	for _, ancestor := range []string{"error", "exception", "token", "password"} {
		var out bytes.Buffer
		logger := slog.New(NewHandler(&HandlerOptions{Output: &out, Format: "json"}))
		logger.LogAttrs(context.Background(), slog.LevelInfo, "event", slog.Group("request", slog.Group(ancestor, slog.Group("inner", slog.String("detail", "PRIVATE_DETAIL")))))
		if strings.Contains(out.String(), "PRIVATE_DETAIL") {
			t.Fatalf("ancestor=%s output=%s", ancestor, out.String())
		}
	}
}
