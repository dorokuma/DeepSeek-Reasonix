#!/usr/bin/env bash
# Validate one stable release tag set and emit the values shared by all release
# workflows. Stable releases are deliberately all-or-nothing: the CLI, npm, and
# desktop tags must already exist on the current main-v2 commit before the sole
# release-environment approval is requested.
set -euo pipefail

release_tag="${RELEASE_TAG:?RELEASE_TAG is required}"
release_remote="${RELEASE_REMOTE:-origin}"

if [[ ! "$release_tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
	echo "::error::stable release tag must be vMAJOR.MINOR.PATCH, got: $release_tag" >&2
	exit 1
fi

version="${release_tag#v}"
cli_tag="$release_tag"
npm_tag="npm-v${version}"
desktop_tag="desktop-v${version}"

head_sha="$(git rev-parse HEAD^{commit})"
main_sha="$(git ls-remote "$release_remote" refs/heads/main-v2 | awk 'NR == 1 { print $1 }')"
if [ -z "$main_sha" ]; then
	echo "::error::cannot resolve $release_remote/main-v2" >&2
	exit 1
fi
if [ "$head_sha" != "$main_sha" ]; then
	echo "::error::$cli_tag points to $head_sha, but $release_remote/main-v2 is $main_sha" >&2
	exit 1
fi

for tag in "$cli_tag" "$npm_tag" "$desktop_tag"; do
	# Prefer the peeled commit for annotated tags; lightweight tags only return
	# the first line. Both forms are valid release refs.
	tag_sha="$(
		git ls-remote --tags "$release_remote" "refs/tags/$tag" "refs/tags/$tag^{}" |
			awk '/\^\{\}$/ { print $1; found = 1; exit } NR == 1 { first = $1 } END { if (!found) print first }'
	)"
	if [ -z "$tag_sha" ]; then
		echo "::error::required stable release tag is missing: $tag" >&2
		exit 1
	fi
	if [ "$tag_sha" != "$head_sha" ]; then
		echo "::error::$tag points to $tag_sha, expected $head_sha" >&2
		exit 1
	fi
done

output_file="${GITHUB_OUTPUT:-/dev/stdout}"
{
	echo "version=$version"
	echo "cli_tag=$cli_tag"
	echo "npm_tag=$npm_tag"
	echo "desktop_tag=$desktop_tag"
	echo "sha=$head_sha"
} >>"$output_file"

echo "stable release resolved: version=$version sha=$head_sha"
