.PHONY: help build test bundle test-gate docker-build-cuda docker-build-cuda-dev clean

help build test bundle test-gate docker-build-cuda docker-build-cuda-dev clean:
	@$(MAKE) -C server $@
