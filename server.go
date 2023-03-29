package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

func (sc *Smithy) LoadTemplates() error {
	t := template.New("")
	files, err := templatefiles.ReadDir("templates")
	if err != nil {
		return err
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".html") {
			continue
		}
		f, err := templatefiles.Open("templates/" + file.Name())
		if err != nil {
			return err
		}
		contents, err := io.ReadAll(f)
		if err != nil {
			return err
		}

		_, err = t.New(file.Name()).Parse(string(contents))
		if err != nil {
			return err
		}
	}
	sc.template = t
	return nil
}

func (sc *Smithy) Render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	sc.template.ExecuteTemplate(w, name+".html", data)
}

func (sc *Smithy) Error(w http.ResponseWriter, err error) {
	sc.Render(w, "error", H{
		"Error": err.Error(),
	})
}

func (sc *Smithy) Reload(w http.ResponseWriter, r *http.Request) {
	sc.LoadAllRepositories()
	fmt.Fprintf(w, "done")
}

func (sc *Smithy) IndexView(w http.ResponseWriter, r *http.Request) {
	repos := sc.GetRepositories()
	sc.Render(w, "index", H{
		"Repos": repos,
	})
}

func (sc *Smithy) NewProject(w http.ResponseWriter, r *http.Request) {

	if r.Method == http.MethodGet {
		sc.Render(w, "new", H{})
		return
	}

	repoName := r.FormValue("name")
	repoPath := filepath.Join(sc.Root, repoName)
	_, err := git.PlainInit(repoPath, true)
	if err != nil {
		sc.Render(w, "error", H{
			"Status": 500,
			"Error":  err.Error(),
		})
	}
	fmt.Fprint(w, repoName)
}

func (sc *Smithy) ImportProject(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		sc.Render(w, "import", H{})
		return
	}
	r.ParseForm()
	name := r.FormValue("name")
	bare := r.FormValue("bare")
	address := r.FormValue("git")
	repoPath := filepath.Join(sc.Root, name)
	isBare := bare == "on"
	repo, err := git.PlainClone(repoPath, isBare, &git.CloneOptions{
		URL: address,
	})
	if err != nil {
		sc.Render(w, "error", H{
			"Status": 500,
			"Error":  err.Error(),
		})
		return
	}
	rwn := RepositoryWithName{
		Name:       name,
		Repository: repo,
		Path:       repoPath,
	}
	sc.AddRepository(rwn)
	sc.Reload(w, r)
}

func (sc *Smithy) RepoView(w http.ResponseWriter, r *http.Request) {
	repoName := sc.GetParam(r, "repo")
	repo, exists := sc.FindRepo(repoName)
	if !exists {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Repository not found",
		})
		return
	}

	branches, err := ListBranches(repo.Repository)
	if err != nil {
		sc.Error(w, err)
		return
	}

	tags, err := ListTags(repo.Repository)
	if err != nil {
		sc.Error(w, err)
		return
	}

	main, revision, err := findMainBranch(repo.Repository)
	if err != nil {
		sc.Error(w, err)
		return
	}
	log.Printf(`%s default branch is "%s"`, repoName, main)
	commitObj, err := repo.Repository.CommitObject(*revision)
	if err != nil {
		sc.Error(w, err)
		return
	}

	readme, err := GetReadmeFromCommit(commitObj)
	var formattedReadme string
	if err != nil {
		formattedReadme = ""
	} else {
		readmeContents, err := readme.Contents()
		if err != nil {
			formattedReadme = ""
		} else {
			formattedReadme = FormatMarkdown(readmeContents)
		}
	}

	sc.Render(w, "repo", H{
		"RepoName": repoName,
		"Branches": branches,
		"Tags":     tags,
		"Readme":   template.HTML(formattedReadme),
		"Repo":     repo,
	})
}

func (sc *Smithy) RefsView(w http.ResponseWriter, r *http.Request) {
	repoName := sc.GetParam(r, "repo")
	repo, exists := sc.FindRepo(repoName)
	if !exists {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Repository not found",
		})
		return
	}

	branches, err := ListBranches(repo.Repository)
	if err != nil {
		branches = []*plumbing.Reference{}
	}

	tags, err := ListTags(repo.Repository)
	if err != nil {
		tags = []*plumbing.Reference{}
	}

	sc.Render(w, "refs", map[string]any{
		"RepoName": repoName,
		"Branches": branches,
		"Tags":     tags,
	})
}

func (sc *Smithy) TreeView(w http.ResponseWriter, r *http.Request) {
	repoName := sc.GetParam(r, "repo")
	repo, exists := sc.FindRepo(repoName)
	if !exists {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Repository not found",
		})
		return
	}

	var err error
	refNameString := sc.GetParam(r, "ref")
	if refNameString == "" {
		refNameString, _, err = findMainBranch(repo.Repository)
		if err != nil {
			sc.Render(w, "error", H{
				"Status": 404,
				"Error":  "Repository not found",
			})
			return
		}
	}

	revision, err := repo.Repository.ResolveRevision(plumbing.Revision(refNameString))
	if err != nil {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Repository not found",
		})
		return
	}

	treePath := sc.GetParam(r, "path")
	parentPath := filepath.Dir(treePath)
	commitObj, err := repo.Repository.CommitObject(*revision)

	if err != nil {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Repository not found",
		})
		return
	}

	tree, err := commitObj.Tree()

	if err != nil {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Repository not found",
		})
		return
	}

	// We're looking at the root of the project.  Show a list of files.
	if treePath == "" {
		sc.Render(w, "tree", H{
			"RepoName": repoName,
			"RefName":  refNameString,
			"Files":    tree.Entries,
			"Path":     treePath,
		})
		return
	}

	out, err := tree.FindEntry(treePath)
	if err != nil {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Repository not found",
		})
		return
	}

	// We found a subtree.
	if !out.Mode.IsFile() {
		subTree, err := tree.Tree(treePath)
		if err != nil {
			sc.Render(w, "error", H{
				"Status": 404,
				"Error":  "Repository not found",
			})
			return
		}

		sc.Render(w, "tree", map[string]any{
			"RepoName":   repoName,
			"ParentPath": parentPath,
			"RefName":    refNameString,
			"SubTree":    out.Name,
			"Path":       treePath,
			"Files":      subTree.Entries,
		})
		return
	}

	file, err := tree.File(treePath)
	if err != nil {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Repository not found",
		})
		return
	}
	contents, err := file.Contents()
	if err != nil {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Repository not found",
		})
		return
	}
	sc.Render(w, "blob", map[string]any{
		"RepoName":   repoName,
		"RefName":    refNameString,
		"File":       out,
		"ParentPath": parentPath,
		"Path":       treePath,
		"Contents":   contents,
	})
}

func (sc *Smithy) LogView(w http.ResponseWriter, r *http.Request) {
	repoName := sc.GetParam(r, "repo")
	repo, exists := sc.FindRepo(repoName)
	if !exists {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Repository not found",
		})
		return
	}

	refName := sc.GetParam(r, "ref")
	if refName == "" {
		defaultBranchName, _, err := findMainBranch(repo.Repository)
		if err != nil {
			sc.Render(w, "error", H{
				"Status": 404,
				"Error":  "Repository not found",
			})
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/%s/log/%s", repoName, defaultBranchName), http.StatusFound)
		return
	}

	revision, err := repo.Repository.ResolveRevision(plumbing.Revision(refName))
	if err != nil {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Repository not found",
		})
		return
	}

	var commits []Commit
	cIter, err := repo.Repository.Log(&git.LogOptions{From: *revision, Order: git.LogOrderCommitterTime})
	if err != nil {
		sc.Error(w, err)
		return
	}

	for i := 1; i <= PAGE_SIZE; i++ {
		commit, err := cIter.Next()

		if err == io.EOF {
			break
		}

		lines := strings.Split(commit.Message, "\n")

		c := Commit{
			Commit:    commit,
			Subject:   lines[0],
			ShortHash: commit.Hash.String()[:8],
		}
		commits = append(commits, c)
	}

	sc.Render(w, "log", H{
		"RepoName": repoName,
		"RefName":  refName,
		"Commits":  commits,
	})
}

func (sc *Smithy) CommitView(w http.ResponseWriter, r *http.Request) {
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
			"Error":  "Repository not found",
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

	changes, err := GetChanges(commitObj)
	if err != nil {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Repository not found",
		})
		return
	}

	formattedChanges, err := FormatChanges(changes)
	if err != nil {
		sc.Render(w, "error", H{
			"Status": 404,
			"Error":  "Repository not found",
		})
		return
	}

	sc.Render(w, "commit", H{
		"RepoName": repoName,
		"Commit":   commitObj,
		"Changes":  template.HTML(formattedChanges),
	})
}

func (sc *Smithy) getInfoRefs(w http.ResponseWriter, r *http.Request) {
	repoName := sc.GetParam(r, "repo")
	repo, _ := sc.FindRepo(repoName)
	repoPath := repo.Path + ""
	log.Printf("getInfoRefs for %s", repoPath)
	service := r.URL.Query().Get("service")
	serviceName := strings.Replace(service, "git-", "", 1)
	log.Println("serviceName:", serviceName)
	w.Header().Set("Content-Type", "application/x-git-"+serviceName+"-advertisement")
	str := "# service=git-" + serviceName
	fmt.Fprintf(w, "%.4x%s\n", len(str)+offset, str)
	fmt.Fprintf(w, "0000")
	c := GitCommand{
		args: []string{serviceName, "--stateless-rpc", "--advertise-refs", repoPath},
	}
	WriteGitToHttp(w, c)
}

func (sc *Smithy) uploadPack(w http.ResponseWriter, r *http.Request) {
	repoName := sc.GetParam(r, "repo")
	repo, _ := sc.FindRepo(repoName)
	repoPath := repo.Path + ""
	log.Printf("uploadPack for %s", repoPath)
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	requestBody, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(404)
		log.Fatal("Error:", err)
		return
	}
	c := GitCommand{
		procInput: bytes.NewReader(requestBody),
		args:      []string{"upload-pack", "--stateless-rpc", repoPath},
	}
	WriteGitToHttp(w, c)
}

func (sc *Smithy) receivePack(w http.ResponseWriter, r *http.Request) {
	repoName := sc.GetParam(r, "repo")
	repo, _ := sc.FindRepo(repoName)
	repoPath := repo.Path + ""
	log.Printf("receivePack for %s", repoPath)
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	requestBody, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(404)
		log.Fatal("Error:", err)
		return
	}
	c := GitCommand{
		procInput: bytes.NewReader(requestBody),
		args:      []string{"receive-pack", "--stateless-rpc", repoPath},
	}
	WriteGitToHttp(w, c)
}
