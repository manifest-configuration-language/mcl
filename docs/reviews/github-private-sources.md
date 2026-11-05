# Private GitHub release assets

Cluster evaluation failed because the `TartHost` CRD was missing from the MCL schema catalog. The CRD is published in `https://github.com/walnuts1018/cluster-api-provider-tart/releases/download/v0.3.16/infrastructure-components.yaml`, but the repository is private and anonymous GitHub requests return HTTP 404. Without authenticated access, any cluster application that loads that schema source cannot complete evaluation.

MCL fetches canonical URLs in the form `https://github.com/OWNER/REPOSITORY/releases/download/TAG/ASSET` with `GITHUB_TOKEN` set in the environment running `mcl update`, `mcl check`, or another command that fetches an uncached dependency. It first requests release metadata from the GitHub API, selects the asset by its exact filename, then requests that asset with the `application/octet-stream` media type. Other URLs keep the existing unauthenticated HTTPS fetch behavior. When GitHub redirects an asset request to file storage, MCL removes the `Authorization` header before following the redirect.

The token is not written to `mcl.lock`, the dependency cache, or error messages. CI and Argo CD CMP environments that need to fetch uncached private release assets must provide a token that can read the owning repository through the `GITHUB_TOKEN` environment variable.

The dependency tests verify URL parsing, the GitHub API request headers, asset selection and download, authorization removal on redirects, and unauthenticated fallback. The feature is published in MCL 0.3.30. Authenticated retrieval of the Tart release assets and schema validation with the locked provider artifacts have been verified in the infra checkout.
