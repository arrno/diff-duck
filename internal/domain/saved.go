package domain

import "time"

// SavedDiffStatus is where a saved diff is in its life.
type SavedDiffStatus string

const (
	SavedDiffRunning SavedDiffStatus = "running"
	SavedDiffDone    SavedDiffStatus = "done"
	SavedDiffFailed  SavedDiffStatus = "failed"
)

// SavedDiff is one requested diff and, once finished, its result. A failed or stopped run
// stays listed, so nothing a user asked for disappears silently.
type SavedDiff struct {
	ID         string          `json:"id"`
	Repository string          `json:"repository"`
	Base       string          `json:"base"`
	Head       string          `json:"head"`
	BaseSHA    string          `json:"baseSha,omitempty"`
	HeadSHA    string          `json:"headSha,omitempty"`
	Status     SavedDiffStatus `json:"status"`
	// Stage says in plain words what a running diff is doing.
	Stage string `json:"stage,omitempty"`
	// Error explains a failure. A finished diff may hold one when the model failed but the
	// structural result is still usable.
	Error      string     `json:"error,omitempty"`
	Overview   string     `json:"overview,omitempty"`
	Files      int        `json:"files"`
	Added      int        `json:"added"`
	Removed    int        `json:"removed"`
	CreatedAt  time.Time  `json:"createdAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// Result is present only when one saved diff is read on its own, not in lists.
	Result *CompressedDiff `json:"result,omitempty"`
}

// Commit is one entry in a repository's history.
type Commit struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
}
