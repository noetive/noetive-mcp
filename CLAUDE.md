- This is a public repo, consider what is Noetive confidential.
- Dependencies are bumped first, always. `go.mod` points at published versions on
  github.com and nothing else: never a `replace` to a sibling checkout, never a
  require on a commit that is not on the dependency's `origin`. Code that needs an
  unreleased change in a dependency does not land here until that change is pushed
  and the require names it. A `replace` makes the build pass on one machine and fail
  for `go install`, the npm wrapper, the image and every outside contributor, and it
  fails for them at the moment they try, not for us at the moment we introduce it.