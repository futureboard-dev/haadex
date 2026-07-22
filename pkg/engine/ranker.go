package engine

import (
	"math"
	"sort"
	"strings"
)

// RankedResult is a search result with a unified score across all layers.
type RankedResult struct {
	Name       string
	Kind       string
	File       string
	Line       int
	Content    string
	Score      float64 // final combined score
	ParentName string
	Layers     []string // which layers found this result
}

// Layer weights — symbolic matches are highest confidence, then trigram, then semantic.
const (
	weightSymbolic  = 1.0
	weightTrigram   = 0.85
	weightSemantic  = 0.75
	multiLayerBonus = 0.1

	// minSemanticScore is the cosine similarity floor for semantic results.
	// Results below this are too low-confidence and introduce noise.
	minSemanticScore float32 = 0.35
)

// RankResults merges results from all three layers, normalizes scores,
// and returns a single list sorted by combined score (descending).
func RankResults(symbolic, trigram []SymbolRow, semantic []SearchResult, query string) []RankedResult {
	type entry struct {
		result    RankedResult
		bestScore float64
		layers    []string
	}

	merged := map[string]*entry{} // key = file:name

	key := func(file, name string) string { return file + ":" + name }

	// --- Symbolic layer: score = 1.0 (exact name match) ---
	for _, s := range symbolic {
		k := key(s.File, s.Name)
		score := weightSymbolic * 1.0
		if e, ok := merged[k]; ok {
			e.layers = append(e.layers, "symbolic")
			if score > e.bestScore {
				e.bestScore = score
			}
		} else {
			merged[k] = &entry{
				result: RankedResult{
					Name: s.Name, Kind: s.Kind, File: s.File,
					Line: s.Line, Content: s.Content, ParentName: s.ParentName,
				},
				bestScore: score,
				layers:    []string{"symbolic"},
			}
		}
	}

	// --- Trigram layer: normalize FTS5 rank to 0-1, then apply weight ---
	if len(trigram) > 0 {
		// FTS5 rank is negative; closer to 0 = better match.
		// Normalize: best rank maps to 1.0, worst to ~0.
		minRank := trigram[0].Rank
		maxRank := trigram[0].Rank
		for _, s := range trigram {
			if s.Rank < minRank {
				minRank = s.Rank
			}
			if s.Rank > maxRank {
				maxRank = s.Rank
			}
		}
		rankRange := maxRank - minRank

		for _, s := range trigram {
			var normalized float64
			if rankRange == 0 {
				normalized = 1.0
			} else {
				// FTS5 rank: more negative = better, so invert.
				normalized = (maxRank - s.Rank) / rankRange
			}
			// Ensure minimum score of 0.3 for any trigram hit
			normalized = math.Max(normalized, 0.3)
			score := weightTrigram * normalized

			k := key(s.File, s.Name)
			if e, ok := merged[k]; ok {
				e.layers = append(e.layers, "trigram")
				if score > e.bestScore {
					e.bestScore = score
				}
			} else {
				merged[k] = &entry{
					result: RankedResult{
						Name: s.Name, Kind: s.Kind, File: s.File,
						Line: s.Line, Content: s.Content, ParentName: s.ParentName,
					},
					bestScore: score,
					layers:    []string{"trigram"},
				}
			}
		}
	}

	// --- Semantic layer: cosine similarity as-is, apply weight ---
	for _, s := range semantic {
		// Drop low-confidence semantic results — they add noise in large indices.
		if s.Score < minSemanticScore {
			continue
		}
		score := weightSemantic * float64(s.Score)

		// Boost structural symbols (functions, classes, types) over plain variables.
		// In large codebases, variable declarations dominate and dilute results.
		score += kindBoost(s.Kind)

		k := key(s.File, s.Name)
		if e, ok := merged[k]; ok {
			e.layers = append(e.layers, "semantic")
			if score > e.bestScore {
				e.bestScore = score
			}
		} else {
			merged[k] = &entry{
				result: RankedResult{
					Name: s.Name, Kind: s.Kind, File: s.File,
					Line: s.Line, Content: s.Content, ParentName: s.ParentName,
				},
				bestScore: score,
				layers:    []string{"semantic"},
			}
		}
	}

	// --- Apply multi-layer bonus and path boosting ---
	queryLower := strings.ToLower(query)
	results := make([]RankedResult, 0, len(merged))
	for _, e := range merged {
		finalScore := e.bestScore

		// Multi-layer agreement bonus
		if len(e.layers) > 1 {
			finalScore += multiLayerBonus * float64(len(e.layers)-1)
		}

		// File-path keyword boosting
		finalScore += pathBoost(queryLower, strings.ToLower(e.result.File))

		e.result.Score = finalScore
		e.result.Layers = e.layers
		results = append(results, e.result)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results
}

// kindBoost returns a small score bonus for structural code symbols.
// Functions, classes, interfaces, and types carry more semantic signal
// than plain variable declarations.
func kindBoost(kind string) float64 {
	switch kind {
	case "file_summary":
		return 0.08
	case "function", "method", "class", "interface", "struct", "type":
		return 0.05
	default:
		return 0
	}
}

// pathBoost returns a score bonus when query keywords match the file path.
func pathBoost(queryLower, fileLower string) float64 {
	var boost float64

	testKeywords := []string{"test", "e2e", "spec"}
	configKeywords := []string{"config", "route", "setting", "middleware"}

	for _, kw := range testKeywords {
		if strings.Contains(queryLower, kw) && strings.Contains(fileLower, kw) {
			boost += 0.15
			break
		}
	}
	for _, kw := range configKeywords {
		if strings.Contains(queryLower, kw) && strings.Contains(fileLower, kw) {
			boost += 0.1
			break
		}
	}

	return boost
}
