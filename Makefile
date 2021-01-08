PROJECT_NAME = clustermenu
MAIN = clustermenu.go
LATESTTAG=$(shell git describe --abbrev=0 --tags | tr -d 'v')
VERSION=`git describe --tags --always --dirty`
GOOS=linux
GOARCH=amd64 arm64

GO = go
LDFLAGS = -ldflags "-X main.Version=${VERSION}"
BUILD_CMD = build $(LDFLAGS)
TEST_CMD = test "./pkg/..."


all: build

test:
	$(GO) $(TEST_CMD)

build:
	$(GO) $(BUILD_CMD)

install:
	$(GO) install

release:
	for os in ${GOOS}; do \
		for arch in ${GOARCH}; do \
			GOOS=$$os GOARCH=$$arch go build ${LDFLAGS} -o ${PROJECT_NAME}-$$os-$$arch; \
		done; \
	done

# packaging, you need the packaging branch for these

# we build binary-only packages and use the static binary in this tarball
clustermenu-$(LATESTTAG).tar.gz: build
	dh_clean || true
	tar --transform="s,^,clustermenu-$(LATESTTAG)/," --owner=0 --group=0 -czf $@ clustermenu debian clustermenu.spec

# consistency with the other linbit projects
debrelease: clustermenu-$(LATESTTAG).tar.gz
