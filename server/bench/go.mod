module github.com/dvcdsys/code-index/server/bench

go 1.24.0

require (
	github.com/go-skynet/go-llama.cpp v0.0.0-20240314183750-6a8041ef6b46
	github.com/odvcencio/gotreesitter v0.0.0-20260423084729-38e2b42712f2
	github.com/philippgille/chromem-go v0.7.0
)

replace github.com/go-skynet/go-llama.cpp => /tmp/go-llama-build
