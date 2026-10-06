# Runtime PRs may change only validated ARG pins. Dependabot manages these two ecosystems.
length > 0 and all(.[];
  .status == "modified"
  and if $author == "github-actions[bot]" then
    .filename == "runtime/Dockerfile"
    and (.patch | type) == "string"
    and ([.patch | split("\n")[] | select(test("^[+-]"))]
      | length > 0 and all(.[]; test(
        "^[+-]ARG ((CODEX_VERSION|CLAUDE_VERSION|GH_VERSION)=(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)|(SKILLS_REVISION|AGENTS_REVISION)=[0-9a-f]{40})$"
      )))
  elif $author == "dependabot[bot]" then
    .filename == "runtime/Dockerfile"
    or (.filename | test("^\\.github/workflows/[^/]+\\.ya?ml$"))
  else false
  end
)
