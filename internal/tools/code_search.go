package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultSearchCodeMaxResults = 10
	maxSearchCodeResults        = 1000
	maxSearchCodeOffset         = 1_000_000
)

type codeMatch struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// searchCodeParams holds parsed parameters for a code search request.
type searchCodeParams struct {
	pattern       string
	fileGlob      string
	maxResults    int
	offset        int
	isRegex       bool
	caseSensitive bool
	re            *regexp.Regexp
	project       string
}

// parseSearchCodeParams extracts and validates search_code parameters from the request.
func parseSearchCodeParams(req *mcp.CallToolRequest) (*searchCodeParams, *mcp.CallToolResult) {
	args, err := parseArgs(req)
	if err != nil {
		return nil, errResult(err.Error())
	}

	maxResults, err := boundedIntegerArg(
		args,
		"max_results",
		defaultSearchCodeMaxResults,
		1,
		maxSearchCodeResults,
	)
	if err != nil {
		return nil, errResult(err.Error())
	}
	offset, err := boundedIntegerArg(args, "offset", 0, 0, maxSearchCodeOffset)
	if err != nil {
		return nil, errResult(err.Error())
	}

	p := &searchCodeParams{
		pattern:       getStringArg(args, "pattern"),
		fileGlob:      getStringArg(args, "file_pattern"),
		maxResults:    maxResults,
		offset:        offset,
		isRegex:       getBoolArg(args, "regex"),
		caseSensitive: getBoolArg(args, "case_sensitive"),
		project:       getStringArg(args, "project"),
	}

	if p.pattern == "" {
		return nil, errResult("pattern is required")
	}

	if p.isRegex {
		pattern := p.pattern
		if !p.caseSensitive && !strings.HasPrefix(pattern, "(?i)") {
			pattern = "(?i)" + pattern
		}
		p.re, err = regexp.Compile(pattern)
		if err != nil {
			return nil, errResult(fmt.Sprintf("invalid regex: %v", err))
		}
	} else if !p.caseSensitive {
		// For literal mode, lowercase the pattern; matching done case-insensitively
		p.pattern = strings.ToLower(p.pattern)
	}

	return p, nil
}

func boundedIntegerArg(args map[string]any, key string, defaultValue, minValue, maxValue int) (int, error) {
	raw, ok := args[key]
	if !ok {
		return defaultValue, nil
	}
	value, ok := raw.(float64)
	if !ok || math.Trunc(value) != value {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", key, minValue, maxValue)
	}
	if value < float64(minValue) || value > float64(maxValue) {
		return 0, fmt.Errorf("%s must be between %d and %d", key, minValue, maxValue)
	}
	return int(value), nil
}

func (s *Server) handleSearchCode(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	params, errRes := parseSearchCodeParams(req)
	if errRes != nil {
		return errRes, nil
	}

	// Resolve project root
	root, err := s.resolveProjectRoot(params.project)
	if err != nil {
		return errResult(fmt.Sprintf("resolve root: %v", err)), nil
	}

	filePaths, err := s.collectSearchFilePaths(params.fileGlob, params.project)
	if err != nil {
		return errResult(fmt.Sprintf("collect files: %v", err)), nil
	}

	// Scan every match so total and has_more are exact, while retaining only
	// the requested page in memory.
	pageMatches := make([]codeMatch, 0, params.maxResults)
	total := 0
	for _, relPath := range filePaths {
		absPath, pathErr := safePath(root, relPath)
		if pathErr != nil {
			return errResult(fmt.Sprintf("resolve indexed file %q: %v", relPath, pathErr)), nil
		}
		searchErr := searchFile(absPath, relPath, params.pattern, params.re, params.isRegex, params.caseSensitive, func(match codeMatch) {
			if total >= params.offset && len(pageMatches) < params.maxResults {
				pageMatches = append(pageMatches, match)
			}
			total++
		})
		if searchErr != nil {
			return errResult(fmt.Sprintf("search indexed file %q: %v", relPath, searchErr)), nil
		}
	}

	hasMore := params.offset+len(pageMatches) < total

	responseData := map[string]any{
		"pattern":     params.pattern,
		"total":       total,
		"limit":       params.maxResults,
		"offset":      params.offset,
		"has_more":    hasMore,
		"matches":     pageMatches,
		"files_count": len(filePaths),
		"_metadata":   s.stdReadGraphMetadata(params.project),
	}
	s.addIndexStatus(responseData)

	result := jsonResult(responseData)
	s.addUpdateNotice(result)
	return result, nil
}

// collectSearchFilePaths gathers indexed file paths, optionally filtered by a glob pattern.
func (s *Server) collectSearchFilePaths(fileGlob, project string) ([]string, error) {
	st, err := s.resolveStore(project)
	if err != nil {
		return nil, fmt.Errorf("resolve store: %w", err)
	}

	projName := s.resolveProjectName(project)
	projects, err := st.ListProjects()
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	files, err := st.FindNodesByLabel(projName, "File")
	if err != nil {
		return nil, fmt.Errorf("list indexed files: %w", err)
	}

	filePaths := make([]string, 0, len(files))
	for _, f := range files {
		if f.FilePath == "" {
			continue
		}
		if fileGlob != "" {
			matched, matchErr := filepath.Match(fileGlob, filepath.Base(f.FilePath))
			if matchErr != nil {
				return nil, fmt.Errorf("invalid file_pattern %q: %w", fileGlob, matchErr)
			}
			if !matched {
				matched = globMatch(fileGlob, f.FilePath)
			}
			if !matched {
				continue
			}
		}
		filePaths = append(filePaths, f.FilePath)
	}

	return filePaths, nil
}

func searchFile(
	absPath, relPath, pattern string,
	re *regexp.Regexp,
	isRegex, caseSensitive bool,
	onMatch func(codeMatch),
) error {
	f, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	lineNum := 0

	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			lineNum++
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")

			var found bool
			switch {
			case isRegex:
				found = re.MatchString(line)
			case caseSensitive:
				found = strings.Contains(line, pattern)
			default:
				// pattern already lowercased in parseSearchCodeParams
				found = strings.Contains(strings.ToLower(line), pattern)
			}

			if found {
				content := strings.TrimSpace(line)
				if len(content) > 200 {
					content = content[:200] + "..."
				}
				onMatch(codeMatch{
					File:    relPath,
					Line:    lineNum,
					Content: content,
				})
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return fmt.Errorf("read: %w", readErr)
		}
	}
}

// globMatch does a simple glob match supporting ** patterns.
func globMatch(pattern, path string) bool {
	if strings.Contains(pattern, "**") {
		// Split pattern on **
		parts := strings.SplitN(pattern, "**", 2)
		prefix := strings.TrimRight(parts[0], "/")
		suffix := strings.TrimLeft(parts[1], "/")

		if prefix != "" && !strings.HasPrefix(path, prefix) {
			return false
		}
		if suffix != "" {
			matched, _ := filepath.Match(suffix, filepath.Base(path))
			return matched
		}
		return true
	}
	matched, _ := filepath.Match(pattern, path)
	return matched
}
