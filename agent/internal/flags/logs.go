/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package flags

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/nodewright/agent/internal/hostfs"
)

const DefaultLogRetention = 5

type logCandidate struct {
	path           string
	timestamp      time.Time
	collisionIndex int
}

// CleanupOldLogs removes all but the newest keep regular files matching the
// literal filename shape. Collision suffixes preserve creation order when
// timestamps are equal; remaining ties are ordered by path.
func CleanupOldLogs(logFiles LogFiles, keep int) error {
	if keep < 0 {
		return fmt.Errorf("log retention must not be negative: %d", keep)
	}
	entries, err := hostfs.ReadDir(logFiles.rootMount, logFiles.directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading log directory %q: %w", logFiles.directory, err)
	}

	files := make([]logCandidate, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, logFiles.prefix) || !strings.HasSuffix(name, logFiles.suffix) {
			continue
		}
		value := strings.TrimSuffix(strings.TrimPrefix(name, logFiles.prefix), logFiles.suffix)
		parsedTimestamp, collisionIndex, ok := parseLogTimestamp(value)
		if !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stating log file %q: %w", filepath.Join(logFiles.directory, name), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		files = append(files, logCandidate{
			path:           filepath.Join(logFiles.directory, name),
			timestamp:      parsedTimestamp,
			collisionIndex: collisionIndex,
		})
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].timestamp.Equal(files[j].timestamp) {
			if files[i].collisionIndex != files[j].collisionIndex {
				return files[i].collisionIndex > files[j].collisionIndex
			}
			return files[i].path < files[j].path
		}
		return files[i].timestamp.After(files[j].timestamp)
	})
	for _, file := range files[min(keep, len(files)):] {
		if err := hostfs.RemoveFile(logFiles.rootMount, file.path); err != nil {
			return fmt.Errorf("removing old log %q: %w", file.path, err)
		}
	}
	return nil
}

func parseLogTimestamp(value string) (time.Time, int, bool) {
	if parsed, collision, ok := parseLogTimestampWithFormat(value, logTimeFormat); ok {
		return parsed, collision, true
	}
	return parseLogTimestampWithFormat(value, legacyLogTimeFormat)
}

func parseLogTimestampWithFormat(value, format string) (time.Time, int, bool) {
	length := len(format)
	if len(value) < length {
		return time.Time{}, 0, false
	}
	parsed, err := time.Parse(format, value[:length])
	if err != nil {
		return time.Time{}, 0, false
	}
	suffix := value[length:]
	if suffix == "" {
		return parsed, 0, true
	}
	if !strings.HasPrefix(suffix, "-") {
		return time.Time{}, 0, false
	}
	index, ok := parsePositiveDecimal(strings.TrimPrefix(suffix, "-"))
	if !ok {
		return time.Time{}, 0, false
	}
	return parsed, index, true
}

func parsePositiveDecimal(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.Atoi(value)
	return parsed, err == nil && parsed > 0
}
