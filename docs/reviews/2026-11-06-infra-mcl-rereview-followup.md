# 2026年11月6日のinfra・MCL再レビュー対応

## 対象

対象はMCLの前方関数参照、名前付き引数に対するLSP操作、CRDのObjectMeta検証と、infraのValkey Operator、Renovate設定、Actions Runner Valuesです。MCLの実装は`e27513538a936bfc7034f4beb7bfd697b4125299`、条件分岐内のmap overlay対応は`aa4d8f987c0826210c3af238ba372116bb279c7a`、Formatter対応は`518a6f3bf54124c548cad8850450b75c723ec72e`でmainへpushしました。MCL 0.4.4をreleaseし、infraの`experiment/mcl-clusterapi`へ適用しました。

## 前方関数参照のLSP解決

MCLの評価器はトップレベル関数の前方参照を許可しますが、SemanticIndexは宣言位置より後の関数を除外していました。そのため、正常な関数呼び出しでもDefinition、References、Rename、Completionが同じ関数を解決できない状態でした。

トップレベルの関数は参照位置にかかわらず候補として解決し、トップレベルのlet宣言は既存どおり宣言順に従うようにしました。Completionも同じ関数可視性を使います。前方関数参照のDefinition、Rename、Completionを回帰テストで確認します。

## 名前付き引数のRename対象

Renameはカーソル位置でローカル変数を先に検索していました。呼び出し元のローカル変数と呼び出し先のパラメータ名が一致すると、名前付き引数上のカーソルが誤って呼び出し元の変数へ解決されます。

名前付き引数のSourceSpan上では、その名前を定義する関数パラメータを先に解決するようにしました。呼び出し元と呼び出し先に同名の`name`がある例を回帰テストに追加しました。

## 名前付き引数のCompletion

補完対象の引数名に`=`がすでにある場合、候補が`name = `を挿入すると入力済みの`=`も残って不正な式になります。複数行呼び出しでは、解析用に挿入する仮引数の後ろにカンマが必要ですが、従来はカンマを含めず、Completion候補を作れませんでした。

既存の`=`がある位置では引数名だけを挿入します。Completion用の仮ソースでは複数行引数の末尾に区切りカンマを加えてから再Parseします。既存の代入記号と複数行呼び出しを個別の回帰テストで確認しました。

## importした関数のパラメータ操作

同一ファイル内の関数だけを調べる既存の名前付き引数解決では、importした関数のパラメータ宣言と呼び出し側を結び付けられません。パラメータ名のRename後にimport先の名前付き引数が古いまま残る可能性があり、Definition、References、Completionもimport境界で一貫しませんでした。

プロジェクト内のimportを解決して関数シグネチャを取得するproject-aware APIを追加し、実際のLSP Completion、Definition、References、Rename経路から利用するようにしました。Renameは関連するworkspace sourceをまとめて更新し、importの曖昧さや解析できないsourceがある場合は部分的な編集を返さず失敗します。JSON-RPC経路で4操作を確認し、曖昧なimportと不正なworkspace sourceに対する安全側の動作もテストしました。

## CRDのObjectMeta検証

CRD Schemaの検証に加えて、MCLがKubernetes API Server相当の`metadata.name`規則を適用していました。MCLのCRD検証責務をSchemaの型、未知フィールド、OpenAPI、CEL検証に限定する方針と一致しないため、`ValidateObjectMeta()`とDNSサブドメイン名検証を削除しました。

CRD Schemaによるmetadataの型・フィールド検証は維持しています。Kubernetesリソースのidentity必須検証と、Render時にCRD scopeに基づいてnamespaceを検証する処理も維持しています。Schemaが許すmetadata.nameをEvaluateとRenderが受理すること、identityの欠落やcluster-scoped CRDへのnamespace指定は引き続き拒否することを回帰テストで確認しました。

## if branch内のmap overlay

Valkey Operatorの`enableServiceMonitor`を直接条件に使い、trueの場合だけ共有ValuesへServiceMonitor設定を追加する書き方は、既存の`with:`と`if`を組み合わせるとparseできませんでした。`branchValueWithExpression()`が`commonValues with:`の末尾のコロンをmap entryと誤認し、値のoverlay式として扱わなかったためです。

branch値が`with:`または`without:`で終わる場合はoverlay式のparserへ渡すようにしました。通常の`key: value`形式のmap branchは従来どおりmapとして扱います。両branchのfield setが異なる場合は結果を`Map<String, Any>`として明示し、静的型検査の期待型を与えます。if branch内のmap overlayがparseされ、上書きされたnested mapがEvaluate後に出力されることを回帰テストで確認しました。この汎用構文をMCLのreleaseへ含め、Valkey OperatorのValues条件に利用します。

MCL FormatterはConditional branchのOverlay patchを`with:`行と同じindentに出力し、再Parseに失敗していました。branch式がOverlayの場合はpatchのlayout depthを1段深くし、`FormatSource()`の出力が正しいMCLとしてParseできることと、Formatterのindentを回帰テストで確認します。

## Valkey Operatorの設定

Valkey Operatorは`standard`と`monitoring`の名前付きValuesを選んでおり、Featureリストによる間接的な設定セット選択が残っていました。`enableServiceMonitor: Bool`を直接受け取り、trueの場合に限り`metrics.serviceMonitor.enabled`を追加する構成へ変更しました。falseの場合のValues構造を保ちつつ、`features.mcl`とHelm renderだけを委譲する`resources.mcl`を削除し、`main.mcl`から`helm.render()`を呼び出します。trueの場合のinline overlayには、上記のMCL構文修正を使用します。

## Renovateの共有Image検出

Renovate Regex Managerが`runnerImage`という特定の変数名に依存していました。Renovate注釈のあるMCL文字列定数を検出する規則へ変更し、変数名に依存せずDocker tagを抽出できるようにしました。タグ付きImageとdigest付きImageの例で`datasource`、`depName`、`currentValue`を確認しました。Renovate設定validatorは成功しましたが、環境にRE2 packageがなく、RenovateはJavaScript RegExpへfallbackしたため、RE2と同一の検証ではありません。

## Actions Runner Valuesの整形

`actions-runners/values.mcl`のネストしたListに残っていた単独の`-`行をMCL Formatterの出力へ揃えました。Manifestの値は変更していません。最終検証でFormatter checkを行い、結果を追記します。

## Runner Imageのタグ差

Runner DockerfileのベースImageは`2.337.0`、Actions Runner ValuesのImage参照は`2.336.0`です。CIの`tag_strategy: from`がベースImageのtagから生成tagを決める構成であることを確認しました。段階的な更新か、更新漏れかを判定する情報がないため、どちらの値も変更していません。Renovateが共有Imageの更新を検出するRegexは追加しましたが、このbranchから更新PRが作成されることまでは確認していません。

## 検証

MCLの`mise run quality:check`は成功し、Go test、golangci-lint、gofmt、WASM checkを通過しました。MCL core CIのrun `42204719661`とrelease workflowのrun `42204730829`も成功し、v0.4.4のCLI artifactとnpm packageを公開しました。条件分岐内のmap overlay parserを含むv0.4.3のrelease workflow `42202048229`と、最初のLSP・CRD修正を含むv0.4.2のrelease workflow `42198959080`も成功しました。

infraの`mise.toml`と`k8s/mcl.lock`をMCL 0.4.4、revision `518a6f3bf54124c548cad8850450b75c723ec72e`へ更新しました。公開直後はmise-versions indexが0.4.4を返さず`mise install`が404になったため、公式GitHub releaseのdarwin/arm64 artifactを取得し、SHA256SUMSとの一致と`mcl --version`を確認してrenderに使いました。MCLの`actions-runners/values.mcl`と`valkey-operator/main.mcl`は`mcl lint`と`mcl fmt --check`を通過しました。

infraの全135配置をMCL 0.4.4でrenderし、135/135成功しました。Valkey Operatorのrender結果ではberryにServiceMonitorが0件、kurumiに1件あり、Bool設定に応じた出力を確認しました。Renovate設定validatorは成功しましたが、環境にRE2 packageがなくJavaScript RegExpへfallbackしました。加えてNode 24でtag付き・digest付きDocker imageの両方について、annotationからdatasource、depName、currentValueを抽出できることを確認しました。

infraの変更は`e3add76d6`としてcommitし、`experiment/mcl-clusterapi`へpushしました。MCLのレビュー文書も検証結果を追記してcommit・pushします。Kubernetesへのdeployは行っていません。Runner Dockerfileの`2.337.0`とActions Runner Valuesの`2.336.0`の差は、意図を判定できないため変更していません。また、Renovateが実際に更新PRを作成するところまでは確認していません。
