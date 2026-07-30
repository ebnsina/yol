package server

import (
	"fmt"

	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/proto"
)

// Converters between what the database stores and what the rest of the code uses. Kept
// together so the pointer handling that nullable columns require lives in one place.

func text(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func orText(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func int32Ptr(value int) *int32 {
	if value == 0 {
		return nil
	}
	converted := int32(value)
	return &converted
}

func int64Ptr(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}

func int32s(values []int) []int32 {
	out := make([]int32, 0, len(values))
	for _, value := range values {
		out = append(out, int32(value))
	}
	return out
}

func routing(mode sqlc.RoutingMode) *sqlc.RoutingMode {
	return &mode
}

// countUnmanaged counts what was already on the machine before we arrived.
func countUnmanaged(containers []proto.Container) int {
	count := 0
	for _, container := range containers {
		if !container.Managed {
			count++
		}
	}
	return count
}

// humanBytes renders memory the way a person would say it.
func humanBytes(bytes int64) string {
	if bytes <= 0 {
		return "an unknown amount"
	}
	return fmt.Sprintf("%.1f GB", float64(bytes)/1e9)
}

func plural(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

func plural2(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
