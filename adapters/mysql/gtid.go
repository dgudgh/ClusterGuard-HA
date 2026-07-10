package mysql

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type GTIDSet struct {
	intervals map[string][]gtidInterval
}

type GTIDComparison struct {
	MissingTransactions uint64
	ErrantTransactions  uint64
}

type gtidInterval struct {
	start uint64
	end   uint64
}

func ParseGTIDSet(value string) (GTIDSet, error) {
	set := GTIDSet{intervals: map[string][]gtidInterval{}}
	if strings.TrimSpace(value) == "" {
		return set, nil
	}

	for _, rawGroup := range strings.Split(value, ",") {
		parts := strings.Split(strings.TrimSpace(rawGroup), ":")
		if len(parts) < 2 || !validGTIDUUID(parts[0]) {
			return GTIDSet{}, fmt.Errorf("invalid GTID group %q", rawGroup)
		}
		uuid := strings.ToLower(parts[0])
		for _, rawInterval := range parts[1:] {
			interval, err := parseGTIDInterval(rawInterval)
			if err != nil {
				return GTIDSet{}, fmt.Errorf("invalid GTID interval %q: %w", rawInterval, err)
			}
			set.intervals[uuid] = append(set.intervals[uuid], interval)
		}
	}

	for uuid, intervals := range set.intervals {
		set.intervals[uuid] = normalizeGTIDIntervals(intervals)
	}
	return set, nil
}

func CompareGTIDSets(primary, candidate GTIDSet) GTIDComparison {
	return GTIDComparison{
		MissingTransactions: transactionDifference(primary, candidate),
		ErrantTransactions:  transactionDifference(candidate, primary),
	}
}

func parseGTIDInterval(value string) (gtidInterval, error) {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" {
		return gtidInterval{}, fmt.Errorf("malformed interval")
	}
	start, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || start == 0 {
		return gtidInterval{}, fmt.Errorf("invalid start")
	}
	end := start
	if len(parts) == 2 {
		if parts[1] == "" {
			return gtidInterval{}, fmt.Errorf("invalid end")
		}
		end, err = strconv.ParseUint(parts[1], 10, 64)
		if err != nil || end == 0 || end < start {
			return gtidInterval{}, fmt.Errorf("invalid end")
		}
	}
	return gtidInterval{start: start, end: end}, nil
}

func normalizeGTIDIntervals(intervals []gtidInterval) []gtidInterval {
	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i].start != intervals[j].start {
			return intervals[i].start < intervals[j].start
		}
		return intervals[i].end < intervals[j].end
	})
	normalized := make([]gtidInterval, 0, len(intervals))
	for _, interval := range intervals {
		if len(normalized) == 0 {
			normalized = append(normalized, interval)
			continue
		}
		last := &normalized[len(normalized)-1]
		if interval.start <= last.end || (last.end < ^uint64(0) && interval.start == last.end+1) {
			if interval.end > last.end {
				last.end = interval.end
			}
			continue
		}
		normalized = append(normalized, interval)
	}
	return normalized
}

func transactionDifference(left, right GTIDSet) uint64 {
	var difference uint64
	for uuid, leftIntervals := range left.intervals {
		difference += intervalTransactionCount(leftIntervals) - overlapTransactionCount(leftIntervals, right.intervals[uuid])
	}
	return difference
}

func intervalTransactionCount(intervals []gtidInterval) uint64 {
	var count uint64
	for _, interval := range intervals {
		count += interval.end - interval.start + 1
	}
	return count
}

func overlapTransactionCount(left, right []gtidInterval) uint64 {
	var count uint64
	for leftIndex, rightIndex := 0, 0; leftIndex < len(left) && rightIndex < len(right); {
		leftInterval := left[leftIndex]
		rightInterval := right[rightIndex]
		start := leftInterval.start
		if rightInterval.start > start {
			start = rightInterval.start
		}
		end := leftInterval.end
		if rightInterval.end < end {
			end = rightInterval.end
		}
		if start <= end {
			count += end - start + 1
		}
		if leftInterval.end < rightInterval.end {
			leftIndex++
		} else {
			rightIndex++
		}
	}
	return count
}

func validGTIDUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}
