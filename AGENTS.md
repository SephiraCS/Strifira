IA NÃO PODE EDITAR ESTE ARQUIVO, APENAS HUMANOS!

# REGRAS
- Nunca adicione atribuição de IA a commits, títulos de PR ou descrições de PR: sem trailers `Co-Authored-By:`, sem avisos do tipo `🤖 Generated with [Claude Code]` ou similares gerados por IA.
- Toda mudança é entregue por meio de um pull request: crie a branch a partir de `origin/main` em uma nova worktree, faça push da branch, abra o PR com a CLI do GitHub (`gh`) tendo `main` como destino, habilite o auto-merge e acompanhe o pipeline de CI (testes, deploy para alpha, release automática e bump de versão) até ficar verde e o deploy ser verificado.

## Git

### Branches
- Crie branches a partir de `origin/main`, nunca da `main` local — ela frequentemente está desatualizada. Execute `git fetch origin` primeiro, depois crie a branch em uma nova worktree usando os prefixos `feat/…`, `fix/…`, `hotfix/…` ou `refactor/…`, e compare suas alterações com `origin/main`.
- Antes de enviar (push), atualize as referências remotas e faça rebase sobre a `origin/main` mais recente, para que o PR nunca contenha código desatualizado.

### Conflitos

Já houve perda de trabalho durante a resolução de conflitos. Sempre que ocorrer um conflito durante um merge, rebase, cherry-pick ou stash apply:

- Pare. Nunca use `--ours`, `--theirs`, `git checkout -- <file>`, `git reset --hard`, `git clean` nem force-push para fazer um conflito desaparecer.
- Para cada arquivo em conflito, informe ao usuário o que foi alterado no nosso lado e em `origin/main`, incluindo as intenções, os commits e os PRs envolvidos. Explique o que seria perdido com cada alternativa de resolução e apresente uma recomendação justificada. Como padrão, preserve `origin/main` e reaplique nossas alterações por cima.
- Aguarde a confirmação do usuário. Combine as alterações dos dois lados sempre que possível e indique explicitamente tudo o que precisar ser descartado.
- Antes de resolver o conflito, salve uma referência de backup, por exemplo `git branch backup/<branch>-<data>`.
- Após resolver, apresente o diff final dos arquivos em conflito e confirme que o build, o lint e os testes foram executados e passaram.

### Pull Requests
- Use a CLI do GitHub (`gh`) para trabalhar com Git e GitHub.
- A CI é o critério de aprovação: não é obrigatório executar testes ou verificações localmente antes de enviar alterações ou abrir um PR.
- Defina `main` como branch de destino. Habilite o auto-merge (`gh pr merge --auto`) assim que todas as verificações passarem; se o auto-merge estiver indisponível no repositório, faça o merge assim que os checks obrigatórios ficarem verdes.
- Revisores: MarioGK e Filippo. O GitHub não aceita o autor do PR como revisor, então nunca inclua quem abriu o PR.
- Ao concluir, envie ao usuário o link do PR.

## Versionamento
- As versões são bumpadas automaticamente: todo push verde na `main` produz a próxima release patch (um commit `chore(release): vX.Y.Z [skip ci]` criado pelo bot de release, com sua tag `vX.Y.Z`). Nunca escreva ou imite esses commits e nunca edite arquivos de versão em um PR.
- Para um bump intencional de minor ou major, defina a versão no `UI/package.json` em um PR; a próxima release continua a partir dessa versão.

## Evidências de alterações visíveis ao usuário

Para cada fluxo da aplicação visível ao usuário que um PR alterar, anexe capturas de tela reais e vídeos curtos da aplicação em execução. Execute-a localmente com `nice -n 19 make dev-reset` (ou `nice -n 19 make dev`); a aplicação serve em `http://localhost:5173`.

- Capture as evidências na versão do PR que foi efetivamente testada — o commit que passou nas verificações.
- Mostre a navegação e as interações que demonstram a alteração, incluindo os estados de sucesso, erro e vazio, quando aplicável.
- Identifique cada captura com o fluxo correspondente e o SHA do commit testado.
- Antes de anexar, oculte segredos e dados pessoais, como tokens, chaves, e-mails, nomes de clientes e informações de pagamento.
- Incorpore ou anexe as evidências no PR com texto alternativo ou uma descrição textual; envie-as como anexos do PR e nunca as versione no repositório. Para alterações visuais, inclua várias capturas de antes e depois.
- Gere novas evidências sempre que o commit mais recente do PR mudar. Nunca fabrique evidências nem apresente capturas antigas ou simuladas como comprovação de funcionamento. Se não for possível obtê-las, explique o motivo no PR. Para alterações exclusivamente documentais, explique por que capturas da aplicação em execução não se aplicam.

## Prioridade de CPU local

- Ao compilar ou testar localmente, execute todo comando de build, teste, lint e
  benchmark com baixa prioridade de CPU, prefixando-o com `nice -n 19` (por
  exemplo, `nice -n 19 make test-fast`). Servidores de desenvolvimento locais e
  watchers também contam.
- Na CI (`CI=true`), use prioridade total de CPU: não faça nada. Nunca adicione
  `nice` ou `renice` a workflows de CI, Makefiles, scripts ou Containerfiles; a
  baixa prioridade é aplicada por quem executa o comando localmente.

## Zero configuração (decisão do usuário em 2026-10-09, não reverter)

- Toda funcionalidade está sempre ativa. Sem feature flags, variáveis on/off,
  configurações opt-in, chaves de configuração booleanas de enabled, toggles de
  deploy ou kill switches em código, configuração ou CI. Apague os switches e
  os caminhos que eles condicionam.
- Toda região sempre serve. Pertencer à rede NetBird admite um nó
  automaticamente; ele descobre todas as regiões, infere a própria região a
  partir de onde está rodando e começa a servir sem etapa de habilitação por
  região.
- O OCI Vault cria, entrega e configura automaticamente todos os segredos de
  produção first-party. Chaves emitidas por vendors (como Stripe e Cloudflare)
  são armazenadas uma única vez no Vault e coletadas automaticamente; ninguém
  copia segredos à mão.
- Valores têm uma única configuração correta no código. Não exponha knobs de
  ajuste. Fakes, lojas de demonstração e skip-auth pertencem apenas a código e
  binários de teste.
- A CI rejeita nova configuração em estilo switch; o ts-reviewer solicita
  alterações. Somente nomes de terceiros inevitáveis podem ter uma exceção
  estreita, com justificativa de uma linha.

Descubra automaticamente os endpoints de autenticação, armazenamento e serviços regionais; o OCI Vault entrega os segredos de aplicação e de integrações de produção.

### Escopo e segurança da migração

Este é o contrato operacional exigido, não uma afirmação de que toda implementação atual já o cumpre. A TS-795 muda documentação e política; switches existentes em produção, entradas de ajuste (tuning) e lacunas na entrega de segredos permanecem como inventário para o trabalho posterior de remoção do COS. A TS-796 remove o orquestrador; a TS-773 trata do TerraBase. Não remova código de produto ou chaves de configuração como parte da TS-795.

Capacidade always-on não contorna autenticação, consentimento do cliente, permissões de repositório, revogação de credenciais, validação, isolamento de recursos, backups ou recuperação revisada. Preserve a identidade de branch/evento, os quality gates e as condições de falha/limpeza na CI.
