# Match only the bot PR whose current head passed validation in this repository.
[.[] | select(
  .state == "open" and .draft == false
  and .head.repo.full_name == $repo and .base.repo.full_name == $repo
  and .base.ref == $base and .head.sha == $sha and .head.ref == $branch
  and .user.type == "Bot"
  and (
    (.user.login == "dependabot[bot]" and (.head.ref | startswith("dependabot/")))
    or (.user.login == "github-actions[bot]"
        and (.head.ref | test("^dependencies/runtime-pins-[0-9]+$")))
  )
)] | if length == 1 then .[0].number else empty end
