.PHONY: help build test bundle test-gate docker-build-cuda clean

help build test bundle test-gate docker-build-cuda clean:
	@$(MAKE) -C server $@
