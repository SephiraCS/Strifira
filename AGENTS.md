IA NÃO PODE EDITAR ESTE ARQUIVO, APENAS HUMANOS!

# Fluxo de entrega por pull request

Estas regras valem para todo trabalho no repositório, incluindo funcionalidades, correções, refatorações e documentação. Toda mudança é entregue por PR para `main`, sem exceção.

## Card no Strifira

- A IA só pode executar qualquer tarefa, incluindo análise, alteração de código, commit ou PR, se houver card criado no Strifira, workspace `sephira`, time `STR`, para ela. Sem card, pare e solicite a criação antes de começar.
- O identificador e o título do card compõem o título do PR.
- Mova o card para `In Progress` ao começar. Enquanto o PR estiver com verificações pendentes, correções ou evidências por completar, mantenha o card nesse estado.
- Com o PR aberto, a CI verde no commit mais recente e as evidências aplicáveis anexadas, mova o card para `In Review`. Use o estado pelo nome; se necessário, use o status `in_review`. Registre no card o link do PR, o SHA validado e que aguarda revisão e merge por Filippo ou Mario.
- Só mova para `Done` após o merge humano e a conclusão das verificações pós-merge aplicáveis. Tarefas exclusivamente de análise, sem alteração no repositório, podem ser concluídas após registrar a entrega no card.

## Atribuição de IA

- Nunca adicione atribuição de IA a commits, títulos de PR ou descrições de PR: sem trailers `Co-Authored-By:`, sem avisos do tipo `🤖 Generated with [Claude Code]` ou similares gerados por IA.

## Trabalho concorrente

Mais de um agente pode estar trabalhando no repositório e nos mesmos ambientes. Trate toda alteração como concorrente:

- Antes de iniciar, execute `git fetch origin` e confira os PRs abertos, os mergeados recentemente, os commits recentes em `main` e os cards em andamento ou revisão no Strifira. Ao detectar sobreposição com outro agente na mesma área, alinhe com o usuário antes de continuar.
- Tarefas que alteram a mesma tela, módulo ou arquivos seguem fila sequencial. Não execute essas tarefas em paralelo nem em branches empilhadas. A próxima só começa após o merge da anterior, em branch nova a partir da `origin/main` atualizada. Áreas independentes podem avançar em paralelo, cada uma com branch e worktree próprias e PR direto para `main`.
- Assuma o trabalho pelo card, movendo-o para `In Progress` ao começar e registrando a entrega ao concluir. Faça commits pequenos e frequentes e evite manter alterações grandes sem push.
- Nunca edite arquivos de PR aberto de outro agente. Se o cruzamento for inevitável, pare e combine com o usuário a ordem de trabalho e merge.
- Antes de cada push, reavalie o estado remoto, os PRs e a concorrência e faça rebase sobre a `origin/main` mais recente. A CI deve validar o novo commit; evidências também precisam corresponder a ele.
- Ao retomar uma sessão, identifique alterações inesperadas com `git log` e `git diff` antes de restaurar ou descartar qualquer conteúdo. Nunca altere branches, worktrees ou PRs de outra sessão.
- Não cancele deploys de outros agentes. Se houver deploy concorrente, aguarde a estabilização e confirme o SHA servido antes de atribuir um resultado ao seu trabalho.

## Git

### Sincronização e detecção de duplicados

- Antes de analisar o repositório, avaliar mudanças pendentes ou escrever código, execute `git fetch origin` e avance a `main` local com `git merge --ff-only origin/main`. O working tree de uma `main` desatualizada não é trabalho pendente: pode ser resquício de PRs já mergeados por outra sessão ou worktree.
- Mudanças não commitadas só contam como trabalho novo depois de comparadas byte a byte com `origin/main` (`git show origin/main:<arquivo> | cmp -s - <arquivo>`). Conteúdo idêntico ao da main é resquício de merge, não trabalho a entregar — restaure o arquivo e siga adiante.
- Antes de criar branch, commit ou PR, confira os PRs abertos (`gh pr list --state open`) e os mergeados recentemente (`gh pr list --state merged --limit 10`). O repositório usa squash merge: `git branch --merged` NÃO detecta branches cujo conteúdo já está na `main` — confie no estado do PR, não na ancestralidade.
- Mantenha a branch e a worktree enquanto o PR estiver aberto, para permitir correções e novas evidências. Após merge ou encerramento do PR, remova somente as branches e worktrees desta sessão, depois de confirmar que não há trabalho não entregue. Branch mergeada que sobra é isca de duplicação.

### Branches
- Crie branches a partir de `origin/main`, nunca da `main` local. Execute `git fetch origin` primeiro, depois crie a branch em uma nova worktree usando os prefixos `feat/…`, `fix/…`, `hotfix/…` ou `refactor/…` seguidos do card, como `feat/str-26-regras-de-pr`, e compare suas alterações com `origin/main`.
- Antes de enviar (push), atualize as referências remotas e faça rebase sobre a `origin/main` mais recente, para que o PR nunca contenha código desatualizado.

### Conflitos

Já houve perda de trabalho durante a resolução de conflitos. Sempre que ocorrer um conflito durante um merge, rebase, cherry-pick ou stash apply:

- Pare. Nunca use `--ours`, `--theirs`, `git checkout -- <file>`, `git reset --hard`, `git clean` nem force-push para fazer um conflito desaparecer.
- Para cada arquivo em conflito, informe ao usuário o que foi alterado no nosso lado e em `origin/main`, incluindo as intenções, os commits e os PRs envolvidos. Explique o que seria perdido com cada alternativa de resolução e apresente uma recomendação justificada. Como padrão, preserve `origin/main` e reaplique nossas alterações por cima.
- Aguarde a confirmação do usuário. Combine as alterações dos dois lados sempre que possível e indique explicitamente tudo o que precisar ser descartado.
- Antes de resolver o conflito, salve uma referência de backup, por exemplo `git branch backup/<branch>-<data>`.
- Após resolver, apresente o diff final dos arquivos em conflito e confirme que o build, o lint e os testes foram executados e passaram.

### Pull requests

- Use a CLI do GitHub (`gh`) para trabalhar com Git e GitHub.
- A CI é o critério de aprovação: não é obrigatório executar testes ou verificações localmente antes de enviar alterações ou abrir um PR.
- A base é `main` desde a abertura. Nunca use `develop` nem outra branch de funcionalidade como destino, nem abra uma pilha de PRs. Confira com `gh pr view <N> --json baseRefName` antes de sinalizar que está pronto.
- Título do PR no formato `<identificador do card> - <título do card>`, reproduzindo o título vigente no Strifira. Exemplo: `STR-10 - Verificar o export e import de workspaces`.
- Descreva o problema, o comportamento resultante, a validação realizada e as limitações relevantes. Evidências visuais ficam inline no corpo do PR, conforme a seção abaixo. Use `--body-file` para preservar a formatação de descrições com várias linhas.
- Acompanhe todos os checks aplicáveis ao SHA mais recente. Só sinalize o PR pronto quando estiverem verdes, com as evidências aplicáveis completas. Jobs ignorados pelas condições dos workflows não contam como testes executados; informe o escopo real da validação.
- Nunca habilite auto-merge (`gh pr merge --auto` ou equivalente) e nunca faça o merge, mesmo com todos os checks verdes. Somente os usuários Filippo e Mario podem aprovar e fazer o merge: a IA abre o PR, acompanha a CI até ficar verde e sinaliza que o PR está pronto para aprovação.
- Revisores: `MarioGK` e `FilippoAntonino`. O GitHub não aceita o autor do PR como revisor, então nunca inclua quem abriu o PR.
- Atualize o card para `In Review` quando o PR estiver pronto. Depois do merge humano, acompanhe a CI de `main`, o deploy e a release conforme a seção seguinte.
- Ao concluir, envie ao usuário o link do PR.

## CI e deploy

- No PR, a CI valida as alterações; não há release nem deploy para alpha pelo simples fato de abrir o PR. Preserve os checks e as condições de execução definidos nos workflows.
- Após o merge humano, o push em `main` dispara a CI e o deploy do Coolify para alpha. O job de release verifica o commit servido antes de gerar a versão seguinte. Acompanhe também falhas de release e deploy, mesmo que os testes estejam verdes.
- Verifique `https://strifira.sephira.app.br/health` e o campo `version`, que informa o SHA servido, comparando com o commit implantado. Após o bump automático, confirme também o rollout do commit de release. Uma resposta HTTP 200 de uma versão anterior não comprova o deploy da mudança.
- Em falhas de deploy, confira os logs e o health check do Coolify e a identidade do commit. Registre a falha no card e no PR e siga as condições de rollback e recuperação existentes. Não contorne verificações nem reinicie ou cancele deploy de outra sessão.
- Separe o estado de PR pronto para revisão do estado de mudança implantada. Não declare release ou deploy verificados antes de haver evidência correspondente.

## Versionamento
- As versões são bumpadas automaticamente: todo push verde na `main` produz a próxima release patch (um commit `chore(release): vX.Y.Z [skip ci]` criado pelo bot de release, com sua tag `vX.Y.Z`). Nunca escreva ou imite esses commits e nunca edite arquivos de versão em um PR.
- Para um bump intencional de minor ou major, defina a versão no `UI/package.json` em um PR; a próxima release continua a partir dessa versão.

## Evidências de alterações visíveis ao usuário

Para cada fluxo da aplicação visível ao usuário que um PR alterar, anexe capturas de tela reais e vídeos curtos da aplicação em execução. Execute-a localmente com `nice -n 19 make dev-reset` (ou `nice -n 19 make dev`); a aplicação serve em `http://localhost:5173`.

- PRs que alteram fluxos visíveis contêm capturas antes e depois. Em mudanças exclusivamente de documentação, backend ou infraestrutura sem alteração de fluxo visível, explique no PR por que a evidência visual não se aplica e apresente a validação correspondente.
- Capture o estado anterior na base identificada e o estado posterior no commit mais recente do PR que passou nas verificações. Identifique o SHA de cada estado. Nunca apresente uma captura da base como evidência do comportamento novo.
- Mostre a navegação e as interações que demonstram a alteração, incluindo os estados de sucesso, erro e vazio, quando aplicável.
- Identifique cada captura com o fluxo correspondente e o SHA do commit testado.
- Antes de anexar, oculte segredos e dados pessoais, como tokens, chaves, e-mails, nomes de clientes e informações de pagamento.
- Toda mídia deve ser um anexo do GitHub, com URL final em `https://github.com/user-attachments/`. Envie os arquivos pelo upload do editor web do GitHub e obtenha as URLs antes de finalizar o corpo do PR. O anexo herda a visibilidade do repositório; não torne evidências privadas públicas.
- Não use caminhos locais ou relativos, `file://`, caminhos de workspace, `localhost`, URLs temporárias ou de outros serviços como origem da mídia no PR. Não publique placeholders nem suponha que `gh pr create --body-file` envie os arquivos citados no texto.
- Exiba as capturas lado a lado em tabela `Antes / Depois`, usando tags HTML `<img>` com texto alternativo e tamanho ajustado nas células. Não use Markdown de imagem ou link dentro das tabelas. Fora delas, use `<img>` ou Markdown de imagem, sempre com a URL final do anexo.

  ```markdown
  | Antes | Depois |
  | :---: | :---: |
  | <img src="https://github.com/user-attachments/assets/URL_ANTES" alt="Fluxo antes, SHA da base" width="49%" /> | <img src="https://github.com/user-attachments/assets/URL_DEPOIS" alt="Fluxo depois, SHA do PR" width="49%" /> |
  ```

- Vídeos devem ser `.mp4` ou `.mov`, enviados como anexos e exibidos no player nativo do GitHub. Use `<video src="URL_FINAL_DO_ANEXO" controls></video>`, acompanhado da descrição do fluxo e do SHA. Não entregue mídia como link de download ou URL solta.
- Antes de executar `gh pr create` ou atualizar a descrição, valide o corpo final: toda mídia tem URL de anexo do GitHub, não há caminhos locais nem placeholders e o formato segue as regras acima.
- Após publicar, confira a renderização na página do PR. Cada imagem e vídeo deve aparecer inline, sem ícone quebrado e sem exigir download para visualizar. Corrija a descrição se necessário.
- Evidências são anexos do PR e nunca são versionadas no repositório. Para alterações visuais, inclua várias capturas de antes e depois, conforme os fluxos afetados.
- Gere novas evidências sempre que o commit mais recente do PR mudar. Nunca fabrique evidências nem apresente capturas antigas ou simuladas como comprovação de funcionamento. Se não for possível obtê-las, explique o motivo no PR.

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
