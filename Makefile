COMMIT_HASH = $(shell git rev-parse --short HEAD)
BRANCH_NAME = $(shell git rev-parse --abbrev-ref HEAD)
RET = $(shell git describe --contains $(COMMIT_HASH) 1>&2 2> /dev/null; echo $$?)
USER = $(shell whoami)
IS_DIRTY_CONDITION = $(shell git diff-index --name-status HEAD | wc -l)

REPO = ghcr.io/nchc-ai
IMAGE = $(notdir $(CURDIR))

ifeq ($(strip $(IS_DIRTY_CONDITION)), 0)
	# if clean,  IS_DIRTY tag is not required
	IS_DIRTY = $(shell echo "")
else
	# add dirty tag if repo is modified
	IS_DIRTY = $(shell echo "-dirty")
endif

# Image Tag rule
# 1. if repo in non-master branch, use branch name as image tag
# 2. if repo in a master branch, but there is no tag, use <username>-<commit-hash>
# 2. if repo in a master branch, and there is tag, use tag
ifeq ($(BRANCH_NAME), main)
	ifeq ($(RET),0)
		TAG = $(shell git describe --contains $(COMMIT_HASH))$(IS_DIRTY)
	else
		TAG = $(USER)-$(COMMIT_HASH)$(IS_DIRTY)
	endif
else
	TAG = $(BRANCH_NAME)$(IS_DIRTY)
endif


run-operator-locally:
	go run main.go --config=./conf/config.json --logtostderr=true -v 4


run-operator-docker:
	docker run -ti --rm $(REPO)/$(IMAGE):$(TAG)

image:
	docker build -t $(REPO)/$(IMAGE):$(TAG) .


clean:
	rm -rf bin/*
