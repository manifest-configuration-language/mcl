# 2026年11月8日 MCLコード品質・性能レビュー

## 対象

`d2e7e50`を基準に、レビューで挙げられたSource Map、Parser、CRD判定、Formatter、依存Module index、Typed IR解析キャッシュ、Typed IR評価器、CLIフラグ、Schema補完、Dependency Lockを確認します。初回実装時のHEADは`beceed2`で、2026年11月8日の追加移行は`8cda402`に記録されています。言語仕様、レンダリング結果、診断内容と順序、公開API、再現性の挙動は変更対象にしません。

## 実装

Source Map生成では、YAML nodeごとの出力範囲をdocument単位で一度計算し、Manifest値と子要素の対応付けから再利用します。行末offsetも出力全体から一度計算し、nodeごとの改行検索を除きます。親nodeの出力範囲は、従来どおり自身と子孫の終端offsetの最大値です。

CRD Schema Catalogが必要か判定する経路では、組み込みGVKの存在確認に`Builtin()`を使わず、型構築を行わない`HasBuiltinSchema()`を使います。Catalogの読み込み条件と評価結果は変わりません。

FormatterのList内Map開始位置を整える後処理では、置換範囲を元sourceに対して昇順に処理し、`strings.Builder`へ一度だけ出力します。置換ごとのsource全体の再構築をなくし、置換内容と改行を維持します。

通常Schema補完、Schema型フィールド列挙、Helm Schema補完で重複していたプロパティ収集・既存候補の除外・ソートを`schemaCompletionPropertyNames()`にまとめます。呼び出し元ごとの既存フィールド除外と候補順を維持します。

Parserでは、トップレベルの宣言順序判定と、`import`・`type`・`param`・`rule`・`fn`・`let`宣言の解析およびAST構築を`parseTopLevelDeclaration()`へ分離しました。トップレベルには行の取得、宣言フェーズの受け渡し、`emit`・`validate`など残りの文の処理を残し、宣言順序、フェーズ遷移、診断文、Source Span、parser helperの呼び出し順を維持します。

Language Serviceのproject FS経路では、読み込んだsource一式を`ProjectSemanticSnapshot`に保持し、各モジュールのASTと`SemanticIndex`を必要になった時点で一度だけ生成します。Definition、Hover、References、Rename、名前付き引数補完は、同じsnapshot内の解析結果を再利用します。構文エラーのあるsourceもsnapshotのsource一覧には保持し、機能ごとの既存の失敗・スキップ動作を維持します。

LSPでは、document versionと`stateGeneration`に対応するsource集合を保持し、同じsource内容のASTと`SemanticIndex`をリクエスト間で共有します。編集中documentのoverlay、import閉包、workspaceの完全snapshotを別々に保持し、通常の補完やDefinitionが無関係な不正sourceに依存しないようにします。workspace-wideなReferencesとRenameでは、source indexが返した対象モジュールとtransitive importerの範囲を維持しながら、完全snapshotの解析結果を再利用します。source indexのrefreshでソース内容、import関係、root、探索範囲が一致する場合は、既存の不変`WorkspaceSnapshot`を再利用します。

Typed IR解析キャッシュでは、`inferType()`のcache hitが`TypeAnalysisFacts`全体を複製せず、利用する`ResolvedType`だけを独立コピーして返します。`inferAnalysis()`のcache hitとmiss保存で行うFactsのコピーは維持します。参照式の解析結果は静的bindingの型・Factsを共有することがあり、Typed IR特殊化はFactsのmap・sliceを書き換えるため、キャッシュ全体を不変化するまではこの境界を外せません。

Typed IR評価器では、`typedExpression.evaluate()`の`typedExpressionCall` caseから呼出し全体を`evaluateCall()`へ移しました。式評価のbudget・allocation chargeはdispatch前に一度だけ行い、callee、引数、Rule、Deferred Call、Lambda、標準関数の評価順とSource Location処理は移動前のままです。複数の責務を一度に動かさず、call経路を独立して読める最小単位として切り出しています。

同じ評価器から`for`式の処理を`evaluateFor()`へ移しました。遅延sourceのcontinuation作成、即時Listの反復予算、Frame scope、子式評価の順序を一つのスコープ処理にまとめています。dispatch段階のallocationとstep予算は`evaluate()`に残し、各式で一度だけ消費します。

Module indexでは、完全snapshot再構築のimport解決を既存の`resolveModuleDependencies()`へ統合しました。import先のソートと重複除去を含め、更新時と完全再構築時で同じ依存一覧を生成します。source hashだけを根拠にimport解決を省略する変更は行っていません。

CLIでは`render`、`check`、`explain`、`diff`、`update`、`fetch`がそれぞれ登録・取得していたparameter、parameter-file、nondeterministic-output flagを共通関数へ集約しました。各コマンドのflag名・説明・既定値・登録順とparameter parsingの診断は維持します。Kubernetes version flagは既存の独立登録を維持しています。

Dependency Lockの`loadRemote()`では、ManifestとChartで重複していた最大サイズの選択を`remoteMaximumSize()`へまとめました。locked fetch、update、offline readの処理順、cache errorの扱い、digest診断は分岐ごとに維持しています。

Runtime scopeでは、parent frameを辿る読み取り専用`runtimeFrame`を追加し、即時`for`反復と遅延`for`解決で各反復の全環境Map cloneをbinding一件のchild frameへ置き換えました。Referenceとoptional ReferenceはFrameを先に検索します。すべてのtyped LambdaはFrameを捕捉し、呼び出し時に引数とlocalをchild frameとして追加するため、呼び出しごとの外側環境Map複製を避けます。runtime value graphをcloneするときは、Frameをmemoizeしてbinding値を既存のclone状態へ通し、Node Identityのclone semanticsを保ちます。Deferred値と動的Map keyは生成時にFrameを既存のMap scopeへflattenするため、これらの継続にFrame pointerは残りません。ローカル測定benchmarkはまだ追加していないため、速度向上の数値は主張しません。

## 今回変更しない項目

Dependency Module indexの差分更新は、import解決がファイル探索結果にも依存するため、source hashだけで再解決を省く設計にはしません。現状は重複処理を共通化する余地を追加調査し、依存解決のキャッシュキーと無効化条件が明確になるまで挙動を変更しません。

Typed IR解析結果のコピーは、解析結果内の型・Facts・FunctionInferenceが複数箇所で変更されるため、所有権と変更箇所を追跡せずに共有へ切り替えません。評価器の巨大なswitchも、遅延評価・Source Location・スコープ捕捉を保つ責務境界を確認してから分割します。

CLIの共通フラグは、コマンドごとに対象パラメータや必要なoptionが異なります。共通化によってCobraのhelp表示、default、flag errorの発生箇所が変わらない単位を確認できるまで、コマンド登録を変更しません。

Dependency Lockの3モードは、キャッシュ破損時の回復動作、外部取得、エラーの型と文面が異なります。モード差を保ったまま重複が減るヘルパーを特定できていないため、今回のコード変更には含めません。

## 検証

前回の変更では`go test ./internal/syntax`、`go test ./internal/engine -count=1`、`go test ./...`が成功しました。Lambda Frameの通常scope捕捉、`for`内捕捉、Node Identity clone対応後に`go test ./internal/engine -count=1`および`go test ./... -count=1`が成功しました。Apple M5 Pro / darwin arm64でnested Map/Listを使うcache-hit benchmarkを比較すると、type-only hitは`601.7 ns/op`・`1568 B/op`・`4 allocs/op`、Facts込みhitは`2120 ns/op`・`5520 B/op`・`13 allocs/op`でした。この値は機種とGoバージョンに依存する一回のローカル測定です。

## 状態

Source Map、CRD判定、Formatter、Schema補完、Parserの`type`・`param`宣言、LSP Snapshot共有、Typed IR type-only cache hit、Typed IR callと`for`式評価の抽出、`for`反復とLambda invocationのFrame、Module依存解決ヘルパーの共有、CLI評価入力flagの共通化、Dependency Lockのサイズ選択共通化を実装しました。`inferAnalysis()`のcopyはbinding aliasと型特殊化の所有権を維持するため残しています。Deferred値と動的Map keyは引き続きMap scopeを捕捉します。Frameは`for`とLambdaの実行範囲で利用し、全runtime scopeを置き換える変更は未実施です。評価器の残りの責務整理は遅延評価、source location、scope captureの境界を確認して段階的に扱います。

## 2026年11月8日の追加実装

Project index、Filesystem、workspace scope、import解決を`internal/project`へ移しました。LSPとLanguage Serviceは`project` APIを直接利用し、`internal/engine`には既存の評価器・呼び出し元向けのaliasと薄い委譲を残しています。評価器の`moduleGraph`はAST依存、import循環検出、モジュール評価を持つため、Project indexとは分離してengineに残します。

確定値モデルを`internal/value`へ移し、`Kind`、`Value`、`Entry`、`Artifact`、`Lifecycle`、`NodeIdentity`、値の型由来情報を所有させました。`Bundle`と遅延評価、Lambda、Rule、ModuleContextは評価時の状態を含むためengineに残しています。`internal/types`は`ResolvedType`とclone・generic substitutionを所有し、`internal/source`はSource Spanと診断・Provenanceの基本型を所有します。既存の公開Facadeはengine alias経由で維持します。

CLIとWASMの評価・出力の共通経路に`internal/app.EvaluationService`を導入しました。CLIはcheck、bundle評価、render前の評価にこのServiceを使い、WASMはcheck、evaluate、renderを委譲します。Cobra、JavaScript、WASM protocol固有の処理は各entrypointに残します。`packages/browser/src/runtime.ts`からrequest scheduler、Worker管理、Project snapshot同期、protocol bridgeを分離し、`MclRuntime`と`MclProject`の公開型を維持しました。

Source Mapの型と処理、および確定値のYAML/JSON直列化を`internal/output`へ移しました。Output packageは`internal/source`と`internal/value`にだけ依存し、engineへの依存はありません。engine側には既存APIとBundleOutputPlanを保つ薄いadapterを残しています。Source Mapでは、既存のYAML nodeごとの範囲計算に加え、行インデントのsegment treeを一度構築し、block scalar終端の探索で後続行を繰り返し走査しないようにしました。末尾空行の扱いを既存実装と一致させるfocused regression testを追加しました。Parserではトップレベル宣言の解析を一つのまとまった処理へ分け、元の宣言順序と診断を維持しました。

OutputのSource Map/serializer移行後も全Go packageの`go test ./... -count=1`と`git diff --check`が成功しました。Project/Value/Application移行時にはBrowserの`pnpm --dir packages/browser test:unit`（13件）と`GOOS=js GOARCH=wasm go build -o /tmp/mcl-wasm-refactor.wasm ./cmd/mcl-wasm`も成功しました。出力・診断の全入力に対するgolden比較や性能ベンチマークは実施していないため、数値上の速度向上は主張しません。

## 継続する設計作業

現在の変更はパッケージ境界を整える段階であり、Compiler・Typed IR・Runtimeの完全分離を完了していません。`typedExpression`は依然としてengineが所有する可変な再帰構造で、安定した`ExprID`を持つ不変Unitへ移行する作業が残っています。型特殊化、遅延継続、Lambda捕捉を同じCompile Unitの寿命で安全に参照する設計が必要です。

`internal/output`はSource Mapと確定値serializerを所有する段階まで切り出しましたが、BundleOutputPlan、Artifactのphase grouping、Kubernetes検証はengineに残っています。`internal/kubernetes`、`internal/adapters/helm`、`internal/adapters/kustomize`はまだ独立していません。これらはValue、Artifact、Kubernetes resource catalogの依存方向を定めてから順に切り出します。`internal/engine`の削除は、Compiler、Runtime、Adapter、Output、公開Facadeの依存が分離し、外部packageからのengine内部参照がなくなった後の最終段階です。
