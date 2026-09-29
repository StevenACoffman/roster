#!/usr/bin/env bash
# shellcheck shell=bash
#
# Generate protobuf definitions from the OneRoster v1.2 JSON Schemas.
#
# Installs protoschemer (github.com/StevenACoffman/protoschemer) if it is not
# already on PATH, then converts every schema in this directory and in
# rosterjson/ into .proto files.
#
# Usage:
#   ./generate-protos.sh              # generate into the default output dir
#   OUT_DIR=/tmp/protos ./generate-protos.sh
#   DRY_RUN=1 ./generate-protos.sh    # print the result, write nothing
#   SKIP_INSTALL=1 ./generate-protos.sh    # use the protoschemer already on PATH

set -euo pipefail

# has_cmd NAME — true if NAME is an executable file on $PATH.
# Ignores shell functions, aliases, and builtins of the same name.
has_cmd() {
  if [ -n "${ZSH_VERSION:-}" ]; then
    builtin whence -p -- "$1" >/dev/null 2>&1
  elif [ -n "${BASH_VERSION:-}" ]; then
    builtin type -P -- "$1" >/dev/null 2>&1
  else
    command -v -- "$1" >/dev/null 2>&1
  fi
}

SCHEMA_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCHEMA_DIR}/../.." && pwd)"

# Where the .proto files land. This is the buf module root for the internal module,
# so the directory path must match the proto package for buf's
# PACKAGE_DIRECTORY_MATCH rule.
OUT_DIR="${OUT_DIR:-${REPO_ROOT}/proto/oneroster/v1p2/v1}"

PROTO_PACKAGE="${PROTO_PACKAGE:-oneroster.v1p2.v1}"
GO_PACKAGE="${GO_PACKAGE:-github.com/StevenACoffman/roster/gen/go/v1p2/v1;onerosterv1p2v1}"
IMPORT_PREFIX="${IMPORT_PREFIX:-oneroster/v1p2/v1}"
PROTOSCHEMER_VERSION="${PROTOSCHEMER_VERSION:-latest}"
HEADER="${HEADER:-Code generated from the OneRoster v1.2 JSON Schemas. DO NOT EDIT.}"

main() {
  ensure_protoschemer

  local staged name_map
  staged="$(mktemp -d)"
  name_map="${staged}.names.json"
  # shellcheck disable=SC2064  # expand now: the paths must survive this scope
  trap "rm -rf '${staged}' '${name_map}'" EXIT

  stage_schemas "${staged}"
  write_name_map "${name_map}"

  if [ -n "${DRY_RUN:-}" ]; then
    run_protoschemer "${staged}" "${name_map}" "${OUT_DIR}" --dry-run
    return 0
  fi

  clean_output "${OUT_DIR}"
  run_protoschemer "${staged}" "${name_map}" "${OUT_DIR}"
  printf '\nGenerated %s .proto files in %s\n' \
    "$(find "${OUT_DIR}" -maxdepth 1 -name '*.proto' | wc -l | tr -d ' ')" "${OUT_DIR}"
}

# ensure_protoschemer installs the pinned protoschemer and reports its version.
#
# It installs on every run rather than reusing whatever is already on PATH. The
# output of this script is committed, so which build produced it has to be a
# property of the script and not of one machine's $GOBIN. A stale binary still
# answers --help, so a capability check cannot tell a current build from an old
# one; only installing can.
#
# Set SKIP_INSTALL to use the protoschemer already on PATH, for testing an
# unreleased build.
ensure_protoschemer() {
  if [ -n "${SKIP_INSTALL:-}" ]; then
    if ! has_cmd protoschemer; then
      printf 'error: SKIP_INSTALL is set but protoschemer is not on PATH.\n' >&2
      return 1
    fi
    printf 'Using protoschemer already on PATH (SKIP_INSTALL set)\n'
    report_protoschemer_version
    return 0
  fi

  if ! has_cmd go; then
    printf 'error: go is not on PATH, so protoschemer cannot be installed.\n' >&2
    printf '       Install Go, or install protoschemer yourself and re-run with\n' >&2
    printf '       SKIP_INSTALL=1:\n' >&2
    printf '       go install github.com/StevenACoffman/protoschemer@%s\n' \
      "${PROTOSCHEMER_VERSION}" >&2
    return 1
  fi

  printf 'Installing protoschemer@%s ...\n' "${PROTOSCHEMER_VERSION}"
  go install "github.com/StevenACoffman/protoschemer@${PROTOSCHEMER_VERSION}"

  if ! has_cmd protoschemer; then
    printf 'error: installed protoschemer but it is not on PATH.\n' >&2
    printf '       Add %s to PATH.\n' "$(go env GOPATH)/bin" >&2
    return 1
  fi
  report_protoschemer_version
}

# report_protoschemer_version records which build generated the output, so a
# surprising diff can be traced to a version rather than guessed at.
report_protoschemer_version() {
  printf 'protoschemer %s (%s)\n' \
    "$(protoschemer version 2>/dev/null | awk '/GitVersion/ {print $2}')" \
    "$(command -v protoschemer)"
}

# stage_schemas copies every schema into one flat directory.
#
# protoschemer reads a single directory and does not recurse, while these
# schemas live in two: the whole-roster payload here, and the per-endpoint
# payloads in rosterjson/.
stage_schemas() {
  local dest="$1" count
  cp "${SCHEMA_DIR}"/*.json "${dest}/"
  cp "${SCHEMA_DIR}"/rosterjson/*.json "${dest}/"
  count="$(find "${dest}" -maxdepth 1 -name '*.json' | wc -l | tr -d ' ')"
  if [ "${count}" -eq 0 ]; then
    printf 'error: no .json schemas found under %s\n' "${SCHEMA_DIR}" >&2
    return 1
  fi
  printf 'Staged %s schema documents\n' "${count}"
}

# write_name_map names the message built from each document's root schema.
#
# The mapping is explicit because the file names are wire artifacts: nothing
# recovers "GetAllUsers" from "getallusers". A document left out of this map
# contributes only its definitions, which is what we want for any schema that
# exists to be referenced.
write_name_map() {
  cat >"$1" <<-'EOF'
		{
		  "onerosterrosteringservicev1p2-getroster-200-responsepayload-schemav1p0": "GetRosterResponse",
		  "onerosterrosteringservicev1p2-getallacademicsessions-200-responsepayload-schemav1p0": "GetAllAcademicSessionsResponse",
		  "onerosterrosteringservicev1p2-getallclasses-200-responsepayload-schemav1p0": "GetAllClassesResponse",
		  "onerosterrosteringservicev1p2-getallcourses-200-responsepayload-schemav1p0": "GetAllCoursesResponse",
		  "onerosterrosteringservicev1p2-getalldemographics-200-responsepayload-schemav1p0": "GetAllDemographicsResponse",
		  "onerosterrosteringservicev1p2-getallenrollments-200-responsepayload-schemav1p0": "GetAllEnrollmentsResponse",
		  "onerosterrosteringservicev1p2-getallgradingperiods-200-responsepayload-schemav1p0": "GetAllGradingPeriodsResponse",
		  "onerosterrosteringservicev1p2-getallorgs-200-responsepayload-schemav1p0": "GetAllOrgsResponse",
		  "onerosterrosteringservicev1p2-getallschools-200-responsepayload-schemav1p0": "GetAllSchoolsResponse",
		  "onerosterrosteringservicev1p2-getallstudents-200-responsepayload-schemav1p0": "GetAllStudentsResponse",
		  "onerosterrosteringservicev1p2-getallteachers-200-responsepayload-schemav1p0": "GetAllTeachersResponse",
		  "onerosterrosteringservicev1p2-getallterms-200-responsepayload-schemav1p0": "GetAllTermsResponse",
		  "onerosterrosteringservicev1p2-getallusers-200-responsepayload-schemav1p0": "GetAllUsersResponse"
		}
	EOF
}

# clean_output removes previously generated .proto files.
#
# Without this, a definition that is renamed or dropped upstream leaves an
# orphaned file behind that still compiles, so nothing ever reports it.
clean_output() {
  local dir="$1" stale
  mkdir -p "${dir}"
  stale="$(find "${dir}" -maxdepth 1 -name '*.proto' | wc -l | tr -d ' ')"
  if [ "${stale}" -gt 0 ]; then
    printf 'Removing %s previously generated .proto files\n' "${stale}"
    find "${dir}" -maxdepth 1 -name '*.proto' -delete
  fi
}

run_protoschemer() {
  local in="$1" name_map="$2" out="$3"
  shift 3
  protoschemer jsonschema \
    --in "${in}" \
    --out "${out}" \
    --proto-package "${PROTO_PACKAGE}" \
    --go-package "${GO_PACKAGE}" \
    --import-prefix "${IMPORT_PREFIX}" \
    --strip-suffix DType \
    --name-map "${name_map}" \
    --header "${HEADER}" \
    "$@"
}

main "$@"
