.PHONY: help build test bundle test-gate docker-build-cuda docker-build-cuda-dev clean

help build test bundle test-gate docker-build-cuda docker-build-cuda-dev clean:
	@$(MAKE) -C server $@

.PHONY: plugin-reload-local
plugin-reload-local:
	@echo "==> Removing cix plugin and code-index marketplace"
	-claude plugin remove cix@code-index
	-claude plugin marketplace remove code-index
	@echo "==> Purging plugin cache"
	rm -rf $(HOME)/.claude/plugins/cache/code-index
	@echo "==> Reinstalling marketplace from $(CURDIR)"
	claude plugin marketplace add $(CURDIR)
	claude plugin install cix@code-index
	@echo "==> Reloaded. Restart Claude Code session to pick up changes."
