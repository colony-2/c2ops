module github.com/creack/pty

go 1.26.10

require github.com/colony-2/c2ops/gha/upstream/pty v1.1.24

// Keep this adapter buildable on its own as well as through the gha module.
replace github.com/colony-2/c2ops/gha/upstream/pty => github.com/creack/pty v1.1.24
