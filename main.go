package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/slack-go/slack"
)

const (
	csvFileName    = "channel_mapping.csv"
	apiTimeout     = 15 * time.Second
	sleepBetween   = time.Second
	rateLimitSleep = 5 * time.Second
	maxRetries     = 3
)

var channelNameRe = regexp.MustCompile(`^[a-z0-9_\-\p{L}\p{N}]{1,80}$`)

type renameEntry struct {
	asis string
	tobe string
}

type channelInfo struct {
	ID         string
	IsArchived bool
}

func main() {
	log.SetFlags(log.Ltime)

	token := os.Getenv("SLACK_USER_TOKEN")
	if token == "" {
		log.Fatal("SLACK_USER_TOKEN environment variable is not set")
	}

	applyMode := strings.ToLower(os.Getenv("APPLY")) == "true"

	client := slack.New(token)

	plan, err := loadCSV(csvFileName)
	if err != nil {
		log.Fatalf("failed to load CSV: %v", err)
	}
	log.Printf("loaded %d rename entries from %s", len(plan), csvFileName)

	channels, err := fetchPublicChannels(client)
	if err != nil {
		log.Fatalf("failed to fetch channels: %v", err)
	}
	log.Printf("fetched %d public channels", len(channels))

	errs, skipped := validatePlan(plan, channels)
	if len(errs) > 0 {
		fmt.Fprintln(os.Stderr, "validation errors:")
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  - %s\n", e)
		}
		os.Exit(1)
	}
	log.Println("validation passed")
	if len(skipped) > 0 {
		fmt.Println("skipped entries:")
		for _, s := range skipped {
			fmt.Printf("  - %s\n", s)
		}
	}

	activePlan := make([]renameEntry, 0, len(plan))
	for _, entry := range plan {
		if ch, ok := channels[entry.asis]; ok && !ch.IsArchived {
			activePlan = append(activePlan, entry)
		}
	}

	fmt.Println("rename plan:")
	for _, entry := range activePlan {
		fmt.Printf("  %s -> %s\n", entry.asis, entry.tobe)
	}

	if !applyMode {
		log.Println("dry-run mode (set APPLY=true to execute)")
		return
	}

	log.Println("starting rename...")
	failed := false
	for i, entry := range activePlan {
		if i > 0 {
			time.Sleep(sleepBetween)
		}
		if err := renameChannel(client, channels[entry.asis], entry.asis, entry.tobe); err != nil {
			fmt.Printf("FAIL: %s -> %s (%v)\n", entry.asis, entry.tobe, err)
			failed = true
		} else {
			fmt.Printf("OK: %s -> %s\n", entry.asis, entry.tobe)
		}
	}

	if failed {
		os.Exit(1)
	}
}

// loadCSV reads channel_mapping.csv and returns a slice of rename entries.
func loadCSV(path string) ([]renameEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.TrimLeadingSpace = true

	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse CSV: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("CSV is empty")
	}

	hdr := records[0]
	if len(hdr) < 2 ||
		strings.ToLower(strings.TrimSpace(hdr[0])) != "asis" ||
		strings.ToLower(strings.TrimSpace(hdr[1])) != "tobe" {
		return nil, fmt.Errorf("CSV header must be 'asis,tobe', got: %v", hdr)
	}
	if len(records) < 2 {
		return nil, errors.New("CSV has no data rows")
	}

	entries := make([]renameEntry, 0, len(records)-1)
	for i, row := range records[1:] {
		lineNum := i + 2
		if len(row) < 2 {
			return nil, fmt.Errorf("line %d: expected 2 columns, got %d", lineNum, len(row))
		}
		asis := strings.TrimSpace(row[0])
		tobe := strings.TrimSpace(row[1])
		if asis == "" {
			return nil, fmt.Errorf("line %d: 'asis' is empty", lineNum)
		}
		if tobe == "" {
			return nil, fmt.Errorf("line %d: 'tobe' is empty", lineNum)
		}
		entries = append(entries, renameEntry{asis: asis, tobe: tobe})
	}
	return entries, nil
}

// validatePlan checks that all rename operations are safe to execute.
// It returns all validation errors and skipped entries (archived channels) without executing any renames.
func validatePlan(plan []renameEntry, channels map[string]channelInfo) (errs []string, skipped []string) {
	// Count tobe targets to detect duplicates.
	tobeCount := make(map[string]int)
	for _, e := range plan {
		tobeCount[e.tobe]++
	}
	duplicatesReported := make(map[string]bool)

	for _, e := range plan {
		ch, ok := channels[e.asis]
		if !ok {
			errs = append(errs, fmt.Sprintf("channel %q not found", e.asis))
			continue
		}
		if ch.IsArchived {
			skipped = append(skipped, fmt.Sprintf("channel %q is archived, skipping", e.asis))
			continue
		}

		if !channelNameRe.MatchString(e.tobe) {
			errs = append(errs,
				fmt.Sprintf("channel name %q is invalid (must match ^[a-z0-9_-]{1,80}$)", e.tobe))
		}

		if e.asis != e.tobe {
			if existing, exists := channels[e.tobe]; exists && !existing.IsArchived {
				errs = append(errs, fmt.Sprintf("target channel %q already exists", e.tobe))
			}
		}

		if tobeCount[e.tobe] > 1 && !duplicatesReported[e.tobe] {
			errs = append(errs, fmt.Sprintf("duplicate tobe target: %q", e.tobe))
			duplicatesReported[e.tobe] = true
		}
	}

	return errs, skipped
}

// fetchPublicChannels retrieves all public channels (including archived) and returns
// a map of channel name to channelInfo.
func fetchPublicChannels(client *slack.Client) (map[string]channelInfo, error) {
	channels := make(map[string]channelInfo)
	cursor := ""

	for {
		ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
		result, nextCursor, err := client.GetConversationsContext(ctx, &slack.GetConversationsParameters{
			Cursor:          cursor,
			ExcludeArchived: false,
			Types:           []string{"public_channel"},
			Limit:           200,
		})
		cancel()

		if err != nil {
			var rle *slack.RateLimitedError
			if errors.As(err, &rle) {
				wait := rle.RetryAfter
				if wait <= 0 {
					wait = rateLimitSleep
				}
				log.Printf("rate limited while fetching channels, retrying after %v", wait)
				time.Sleep(wait)
				continue
			}
			return nil, fmt.Errorf("GetConversationsContext: %w", err)
		}

		for _, ch := range result {
			channels[ch.Name] = channelInfo{ID: ch.ID, IsArchived: ch.IsArchived}
		}

		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	return channels, nil
}

// renameChannel renames a channel with retry on rate-limit errors.
func renameChannel(client *slack.Client, ch channelInfo, asis, tobe string) error {
	for attempt := 1; attempt <= maxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
		_, err := client.RenameConversationContext(ctx, ch.ID, tobe)
		cancel()

		if err == nil {
			return nil
		}

		var rle *slack.RateLimitedError
		if errors.As(err, &rle) {
			wait := rle.RetryAfter
			if wait <= 0 {
				wait = rateLimitSleep
			}
			log.Printf("rate limited renaming %s -> %s, retrying after %v (attempt %d/%d)",
				asis, tobe, wait, attempt, maxRetries)
			time.Sleep(wait)
			continue
		}

		return err
	}

	return fmt.Errorf("exceeded max retries (%d) for %s -> %s", maxRetries, asis, tobe)
}
