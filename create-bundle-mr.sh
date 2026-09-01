#!/bin/sh
# Create a merge request in an external repository with updated certificate bundle
# Uses GitLab API for efficient single-file updates without cloning
# Usage: ./create-bundle-mr.sh <tag> <repo_path> <cert_file> <bundle_file>

set -e

TAG="$1"
REPO_PATH="$2"
TARGET_CERTIFICATE_PATH="$4"
SOURCE_BUNDLE_PATH="$4"

if [ -z "$TAG" ] || [ -z "$REPO_PATH" ] || [ -z "$TARGET_CERTIFICATE_PATH" ] || [ -z "$SOURCE_BUNDLE_PATH" ]; then
  echo "Usage: $0 <tag> <repo_path> <cert_file> <bundle_file>"
  exit 1
fi

if [ ! -f "$SOURCE_BUNDLE_PATH" ]; then
  echo "ERROR: Bundle file not found: $SOURCE_BUNDLE_PATH"
  exit 1
fi

# Require a GitLab API token with write access to the target project.
if [ -z "$GITLAB_TOKEN" ]; then
  echo "ERROR: GITLAB_TOKEN not set"
  echo "Set a masked, protected CI/CD variable containing a project, group, or personal access token with api scope."
  exit 1
fi

GITLAB_API="${GITLAB_API:-https://gitlab.mgmlab.net/api/v4}"
GITLAB_URL="${GITLAB_URL:-${GITLAB_API%/api/v4}}"
BRANCH_NAME="chore/update-ca-bundle-${TAG}"
COMMIT_MSG="chore: update CA certificate bundle from custom-certs ${TAG}"
MR_TITLE="chore: update CA certificate bundle from custom-certs ${TAG}"

# URL encode the repo path for API calls
ENCODED_PATH=$(echo "$REPO_PATH" | sed 's/\//%2F/g')

echo "Getting project info for ${REPO_PATH}..."
PROJECT_RESPONSE=$(curl -s -H "PRIVATE-TOKEN: ${GITLAB_TOKEN}" \
  "${GITLAB_API}/projects/${ENCODED_PATH}")

PROJECT_ID=$(echo "$PROJECT_RESPONSE" | grep -o '"id":[0-9]*' | head -1 | cut -d: -f2)
PROJECT_DEFAULT_BRANCH=$(echo "$PROJECT_RESPONSE" | grep -o '"default_branch":"[^"]*"' | head -1 | cut -d'"' -f4)

if [ -z "$PROJECT_ID" ] || [ -z "$PROJECT_DEFAULT_BRANCH" ]; then
  echo "ERROR: Could not find project ${REPO_PATH}"
  echo "Response: $PROJECT_RESPONSE"
  exit 1
fi

echo "Found project ID: ${PROJECT_ID}"
TARGET_BRANCH="${TARGET_BRANCH:-$PROJECT_DEFAULT_BRANCH}"
echo "Using target branch: ${TARGET_BRANCH}"

# Check if branch exists
echo "Checking if branch ${BRANCH_NAME} exists..."
ENCODED_BRANCH_NAME=$(echo "$BRANCH_NAME" | sed 's/\//%2F/g')
BRANCH_RESPONSE=$(curl -s -H "PRIVATE-TOKEN: ${GITLAB_TOKEN}" \
  "${GITLAB_API}/projects/${PROJECT_ID}/repository/branches/${ENCODED_BRANCH_NAME}")

BRANCH_COMMIT=$(echo "$BRANCH_RESPONSE" | grep -o '"commit":{[^}]*' | head -1)

if [ -z "$BRANCH_COMMIT" ]; then
  echo "Branch does not exist, will create from ${TARGET_BRANCH}..."
  SOURCE_BRANCH="$TARGET_BRANCH"
else
  echo "Branch exists, will update existing branch"
  SOURCE_BRANCH="$BRANCH_NAME"
fi

# Reuse an existing MR when the release job is retried.
EXISTING_MR=$(curl -s -G -H "PRIVATE-TOKEN: ${GITLAB_TOKEN}" \
  --data-urlencode "source_branch=${BRANCH_NAME}" \
  --data-urlencode "target_branch=${TARGET_BRANCH}" \
  --data-urlencode "state=opened" \
  "${GITLAB_API}/projects/${PROJECT_ID}/merge_requests" | \
  grep -o '"iid":[0-9]*' | head -1 | cut -d: -f2)

# Get the current target file so retries do not create empty commits.
ENCODED_TARGET_CERTIFICATE_PATH=$(echo "$TARGET_CERTIFICATE_PATH" | sed 's/\//%2F/g')
PAYLOAD_FILE=$(mktemp)
CURRENT_FILE_RESPONSE=$(mktemp)
CURRENT_BUNDLE_FILE=$(mktemp)
trap 'rm -f "$PAYLOAD_FILE" "$CURRENT_FILE_RESPONSE" "$CURRENT_BUNDLE_FILE"' EXIT HUP INT TERM
FILE_UPDATE_REQUIRED=1
FILE_STATUS=$(curl -s -o "$CURRENT_FILE_RESPONSE" -w '%{http_code}' \
  -H "PRIVATE-TOKEN: ${GITLAB_TOKEN}" \
  "${GITLAB_API}/projects/${PROJECT_ID}/repository/files/${ENCODED_TARGET_CERTIFICATE_PATH}?ref=${SOURCE_BRANCH}")

if [ "$FILE_STATUS" = "200" ]; then
  FILE_METHOD="PUT"
  CURRENT_FILE_CONTENT=$(grep -o '"content":"[^"]*"' "$CURRENT_FILE_RESPONSE" | head -1 | cut -d'"' -f4)
  if [ -z "$CURRENT_FILE_CONTENT" ] || ! printf '%s' "$CURRENT_FILE_CONTENT" | base64 -d > "$CURRENT_BUNDLE_FILE" 2>/dev/null; then
    echo "ERROR: Could not decode the current ${TARGET_CERTIFICATE_PATH} content"
    exit 1
  fi
  if cmp -s "$SOURCE_BUNDLE_PATH" "$CURRENT_BUNDLE_FILE"; then
    echo "✓ ${TARGET_CERTIFICATE_PATH} already matches the source bundle"
    if [ -n "$EXISTING_MR" ]; then
      echo "✓ Existing MR: ${GITLAB_URL}/${REPO_PATH}/-/merge_requests/${EXISTING_MR}"
      exit 0
    elif [ "$SOURCE_BRANCH" = "$TARGET_BRANCH" ]; then
      echo "✓ No update or merge request is needed"
      exit 0
    else
      echo "✓ ${TARGET_CERTIFICATE_PATH} already matches the source bundle"
      echo "Creating the missing merge request for ${BRANCH_NAME}..."
      FILE_UPDATE_REQUIRED=0
    fi
  fi
  echo "Updating ${TARGET_CERTIFICATE_PATH} in ${REPO_PATH}..."
elif [ "$FILE_STATUS" = "404" ]; then
  FILE_METHOD="POST"
  echo "Creating ${TARGET_CERTIFICATE_PATH} in ${REPO_PATH}..."
else
  echo "ERROR: Could not check whether ${TARGET_CERTIFICATE_PATH} exists (HTTP ${FILE_STATUS})"
  exit 1
fi

if [ "$FILE_UPDATE_REQUIRED" -eq 1 ]; then
  {
  printf '{\n  "branch": "%s",\n  "start_branch": "%s",\n  "content": "' \
    "$BRANCH_NAME" "$SOURCE_BRANCH"
  base64 "$SOURCE_BUNDLE_PATH" | tr -d '\n'
  printf '",\n  "encoding": "base64",\n  "commit_message": "%s",\n' "$COMMIT_MSG"
  printf '  "last_commit_id": null\n}\n'
  } > "$PAYLOAD_FILE"

  FILE_UPDATE=$(curl -s -X "$FILE_METHOD" \
  -H "PRIVATE-TOKEN: ${GITLAB_TOKEN}" \
  -H "Content-Type: application/json" \
  "${GITLAB_API}/projects/${PROJECT_ID}/repository/files/${ENCODED_TARGET_CERTIFICATE_PATH}" \
  --data-binary "@${PAYLOAD_FILE}")

  FILE_PATH=$(echo "$FILE_UPDATE" | grep -o '"file_path":"[^"]*"' | cut -d'"' -f4)

  if [ -z "$FILE_PATH" ]; then
    echo "ERROR updating file:"
    echo "$FILE_UPDATE"
    if echo "$FILE_UPDATE" | grep -q '403 Forbidden'; then
      echo "The GITLAB_TOKEN owner needs Developer or higher access to ${REPO_PATH} and permission to create ${BRANCH_NAME}."
    fi
    exit 1
  fi

  echo "✓ File ${FILE_METHOD} completed in branch ${BRANCH_NAME}"
fi

if [ -n "$EXISTING_MR" ]; then
  echo "✓ Existing MR: ${GITLAB_URL}/${REPO_PATH}/-/merge_requests/${EXISTING_MR}"
  exit 0
fi

echo "Creating merge request..."
{
  printf '{\n  "source_branch": "%s",\n  "target_branch": "%s",\n  "title": "%s",\n' \
    "$BRANCH_NAME" "$TARGET_BRANCH" "$MR_TITLE"
  printf '  "description": "Updates the certificate bundle with the latest from the custom-certs release.\\n\\nGenerated from: custom-certs %s\\nCA bundle source: https://gitlab.mgmlab.net/seksjon-for-bioinformatikk/custom-certs/-/releases/%s"\n}\n' \
    "$TAG" "$TAG"
} > "$PAYLOAD_FILE"

MR_RESPONSE=$(curl -s -X POST \
  -H "PRIVATE-TOKEN: ${GITLAB_TOKEN}" \
  -H "Content-Type: application/json" \
  "${GITLAB_API}/projects/${PROJECT_ID}/merge_requests" \
  --data-binary "@${PAYLOAD_FILE}")
MR_IID=$(echo "$MR_RESPONSE" | grep -o '"iid":[0-9]*' | head -1 | cut -d: -f2)

if [ -z "$MR_IID" ]; then
  echo "ERROR creating merge request:"
  echo "$MR_RESPONSE"
  exit 1
fi

echo "✓ Merge request created: ${GITLAB_URL}/${REPO_PATH}/-/merge_requests/${MR_IID}"

