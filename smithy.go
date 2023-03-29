package main

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/alecthomas/chroma/formatters/html"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting"
)

//go:embed templates
var templatefiles embed.FS

var (
	offset        = 5
	PAGE_SIZE int = 500
)

type Smithy struct {
	Root        string
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	repos       map[string]RepositoryWithName
	template    *template.Template
}

func NewSmithy(root string) Smithy {
	return Smithy{
		Root:        root,
		Title:       "Liu Song’s Projects",
		Description: "Publish your git repositories with ease",
	}
}

func (sc *Smithy) AddRepository(rwn RepositoryWithName) {
	sc.repos[rwn.Name] = rwn
}

func (sc *Smithy) LoadAllRepositories() (err error) {
	files, err := os.ReadDir(sc.Root)
	if err != nil {
		return
	}
	sc.repos = make(map[string]RepositoryWithName)
	for _, f := range files {
		repoPath := path.Join(sc.Root, f.Name())
		r, err := git.PlainOpen(repoPath)
		if err != nil {
			continue
		}
		key := f.Name()
		rwn := RepositoryWithName{
			Name:       f.Name(),
			Repository: r,
			Path:       repoPath,
		}
		sc.repos[key] = rwn
	}
	return
}

func (sc *Smithy) GetRepositories() []RepositoryWithName {
	var repos []RepositoryWithName
	for _, repo := range sc.repos {
		repos = append(repos, repo)
	}
	sort.Sort(RepositoryByName(repos))
	return repos
}

func (sc *Smithy) FindRepo(slug string) (RepositoryWithName, bool) {
	value, exists := sc.repos[slug]
	return value, exists
}

type GitCommand struct {
	procInput *bytes.Reader
	args      []string
}

type H = map[string]interface{}

type RepositoryWithName struct {
	Name       string
	Path       string
	Repository *git.Repository
}

type RepositoryByName []RepositoryWithName

func (r RepositoryByName) Len() int      { return len(r) }
func (r RepositoryByName) Swap(i, j int) { r[i], r[j] = r[j], r[i] }
func (r RepositoryByName) Less(i, j int) bool {
	res := strings.Compare(r[i].Name, r[j].Name)
	return res < 0
}

type ReferenceByName []*plumbing.Reference

func (r ReferenceByName) Len() int      { return len(r) }
func (r ReferenceByName) Swap(i, j int) { r[i], r[j] = r[j], r[i] }
func (r ReferenceByName) Less(i, j int) bool {
	res := strings.Compare(r[i].Name().String(), r[j].Name().String())
	return res < 0
}

type Commit struct {
	Commit    *object.Commit
	Subject   string
	ShortHash string
}

func (c *Commit) FormattedDate() string {
	return c.Commit.Author.When.Format("2006-01-02")
	// return c.Commit.Author.When.Format(time.RFC822)
}

func ReferenceCollector(it storer.ReferenceIter) ([]*plumbing.Reference, error) {
	var refs []*plumbing.Reference

	for {
		b, err := it.Next()

		if err == io.EOF {
			break
		}

		if err != nil {
			return refs, err
		}

		refs = append(refs, b)
	}
	sort.Sort(ReferenceByName(refs))
	return refs, nil
}

func ListBranches(r *git.Repository) ([]*plumbing.Reference, error) {
	it, err := r.Branches()
	if err != nil {
		return []*plumbing.Reference{}, err
	}
	return ReferenceCollector(it)
}

func ListTags(r *git.Repository) ([]*plumbing.Reference, error) {
	it, err := r.Tags()
	if err != nil {
		return []*plumbing.Reference{}, err
	}
	return ReferenceCollector(it)
}

func GetReadmeFromCommit(commit *object.Commit) (*object.File, error) {
	options := []string{
		"readme",
		"README",
		"readme.md",
		"README.md",
		"readme.txt",
		"README.txt",
		"readme.markdown",
		"README.markdown",
	}

	for _, opt := range options {
		f, err := commit.File(opt)

		if err == nil {
			return f, nil
		}
	}
	return nil, errors.New("no valid readme")
}

func FormatMarkdown(input string) string {
	var buf bytes.Buffer
	markdown := goldmark.New(
		goldmark.WithExtensions(
			highlighting.NewHighlighting(
				highlighting.WithFormatOptions(
					html.WithClasses(true),
				),
			),
		),
	)
	if err := markdown.Convert([]byte(input), &buf); err != nil {
		return input
	}
	return buf.String()
}

func (sc *Smithy) GetParam(r *http.Request, name string) (out string) {
	return r.Context().Value(ParamsKey).(map[string]string)[name]
}

func findMainBranch(repo *git.Repository) (string, *plumbing.Hash, error) {
	branches, _ := ListBranches(repo)

	if len(branches) == 0 {
		return "", nil, errors.New("no branches found")
	}

	var branch string
	for _, br := range branches {
		if br.Name().Short() == "main" || br.Name().Short() == "master" {
			branch = br.Name().Short()
			break
		}
	}
	if branch == "" {
		branch = branches[0].Name().Short()
	}
	revision, err := repo.ResolveRevision(plumbing.Revision(branch))
	return branch, revision, err
}

func GetChanges(commit *object.Commit) (object.Changes, error) {
	var changes object.Changes
	var parentTree *object.Tree

	parent, err := commit.Parent(0)
	if err == nil {
		parentTree, err = parent.Tree()
		if err != nil {
			return changes, err
		}
	}

	currentTree, err := commit.Tree()
	if err != nil {
		return changes, err
	}

	return object.DiffTree(parentTree, currentTree)

}

// PatchHTML returns an HTML representation of a patch
func PatchHTML(p object.Patch) string {
	buf := bytes.NewBuffer(nil)
	ue := NewUnifiedEncoder(buf, DefaultContextLines)
	err := ue.Encode(p)
	if err != nil {
		fmt.Println("PatchHTML error")
	}
	return buf.String()
}

// FormatChanges spits out something similar to `git diff`
func FormatChanges(changes object.Changes) (string, error) {
	var s []string
	for _, change := range changes {
		patch, err := change.Patch()
		if err != nil {
			return "", err
		}
		s = append(s, PatchHTML(*patch))
	}

	return strings.Join(s, "\n\n\n\n"), nil
}

func (sc *Smithy) PatchView(w http.ResponseWriter, r *http.Request) {
	repoName := sc.GetParam(r, "repo")
	repo, exists := sc.FindRepo(repoName)
	if !exists {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Repository not found",
		})
		return
	}

	commitID := sc.GetParam(r, "hash")

	if commitID == "" {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Commit not found",
		})
		return
	}

	commitHash := plumbing.NewHash(commitID)
	commitObj, err := repo.Repository.CommitObject(commitHash)
	if err != nil {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Repository not found",
		})
		return
	}

	var patch string
	if commitObj.NumParents() == 0 {
		sc.Error(w, err)
		return
	} else {
		parentCommit, err := commitObj.Parent(0)

		if err != nil {
			sc.Render(w, "error", H{
				"Error": err.Error(),
			})
			return
		}

		patchObj, err := parentCommit.Patch(commitObj)
		if err != nil {
			sc.Render(w, "error", H{
				"Error": err.Error(),
			})
			return
		}
		patch = patchObj.String()
	}

	const commitFormatDate = "Mon, 2 Jan 2006 15:04:05 -0700"
	commitHashStr := fmt.Sprintf("From %s Mon Sep 17 00:00:00 2001", commitObj.Hash)
	from := fmt.Sprintf("From: %s <%s>", commitObj.Author.Name, commitObj.Author.Email)
	date := fmt.Sprintf("Date: %s", commitObj.Author.When.Format(commitFormatDate))
	subject := fmt.Sprintf("Subject: [PATCH] %s", commitObj.Message)

	stats, err := commitObj.Stats()
	if err != nil {
		sc.Error(w, err)
		return
	}
	fmt.Fprintf(w, "%s\n%s\n%s\n%s\n---\n%s\n%s", commitHashStr, from, date, subject, stats.String(), patch)
}

func WriteGitToHttp(w http.ResponseWriter, gitCommand GitCommand) {
	cmd := exec.Command("git", gitCommand.args...)
	stdout, err := cmd.StdoutPipe()
	log.Printf("WriteGitToHttp: %v", cmd)
	if err != nil {
		w.WriteHeader(404)
		log.Fatal("Error:", err)
		return
	}

	if gitCommand.procInput != nil {
		cmd.Stdin = gitCommand.procInput
	}

	if err := cmd.Start(); err != nil {
		w.WriteHeader(404)
		log.Fatal("Error:", err)
		return
	}

	nbytes, err := io.Copy(w, stdout)
	if err != nil {
		log.Fatal("Error writing to socket", err)
	} else {
		log.Printf("Bytes written: %d", nbytes)
	}
}
