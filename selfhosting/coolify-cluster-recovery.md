# Recuperação do cluster Strifira no Coolify

## Diagnóstico da STR-27

Em 10/11/2026, os quatro containers estavam conectados à rede Docker `coolify`.
Os seeds NATS apontavam ao UUID do aplicativo, mas o DNS dessa rede registrava
somente nomes com sufixo de deploy. Por exemplo, um endereço `nats://<uuid>:6222`
não encontrava o container `<uuid>-20261110T200359`. A inspeção de
`NetworkSettings.Networks.coolify.Aliases` confirmou a ausência dos UUIDs.
Os logs mostravam falhas de DNS e espera por rotas. `/health` respondia 200,
mas `/ready` respondia 503 e o bucket de arquivos permanecia indisponível.

`connect_to_docker_network=false` não era a causa nesse modo de implantação.
O gerador Dockerfile do Coolify já usa a rede de destino. Os aliases estáveis
precisam ser registrados nessa rede, sem publicar a porta 6222 no host.

Somente o aplicativo principal tinha volume persistente em `/app/uploads`.
Cada réplica tinha arquivos JetStream no filesystem do container. Além disso,
os deployments usavam nomes temporários e permitiam sobreposição entre o NATS
antigo e o novo sobre o mesmo volume do nó principal. Cada diretório JetStream
deve ter um único processo proprietário e um volume separado por nó.

O teste integrado também reproduziu falha após recriar um nó: o backend
gerava outro nome NATS para o mesmo estado raft. O PR persiste esse nome em
`uploads/.jetstream/.server-name` e reabre os buckets existentes sem reduzir a
replicação. A reconciliação só aumenta a redundância, inclusive quando a visão
de membros ainda está incompleta durante um restart.

## Estado declarativo e plano revisável

`scripts/reconcile_coolify_cluster.py` descobre o aplicativo de produção pelo
domínio e os nós pelo repositório, branch `main`, ambiente e destino comuns.
O token de administração já provisionado é lido somente de `COOLIFY_API_KEY`.
Não o copie para arquivos de plano, shell history, comentários ou o repositório.

```sh
python3 scripts/reconcile_coolify_cluster.py plan > /tmp/strifira-cluster-plan.json
```

O comando só lê a API. O plano lista os valores anteriores e os valores
necessários para cada nó:

- Alias DNS igual ao UUID, preservando aliases existentes.
  A API devolve aliases separados por vírgulas, embora aceite JSON na escrita.
  O reconciliador usa o formato retornado para verificar a aplicação idempotente.
- Seeds para todos os outros nós. O bus atual usa `NoAdvertise`, portanto não
  basta uma topologia em estrela para obter as rotas entre todas as réplicas.
- Healthcheck HTTP em `http://localhost:3000/ready`, incluindo a prontidão NATS.
  O campo do Coolify `health_check_enabled` permanece sempre `true`.
- Um volume exclusivo em `/app/uploads`. O volume existente nunca é substituído.
- `is_consistent_container_name_enabled=true`, nome obrigatório da API Coolify
  para parar o container anterior antes de iniciar outro sobre o mesmo volume.
- Revisão `HEAD` da `main` e inclusão do SHA no build de todos os nós. As réplicas
  não devem continuar congeladas em uma revisão antiga com a mesma base de dados.

O plano exclui segredos e variáveis de preview. Ele recusa deploys concorrentes,
aliases em conflito, volumes compartilhados e configurações ambíguas. Não muda
domínios, autenticação, credenciais, labels, limites de recursos ou outros envs.

Depois de revisar e autorizar a intervenção:

```sh
python3 scripts/reconcile_coolify_cluster.py apply /tmp/strifira-cluster-plan.json
```

`apply` compara o plano com a configuração atual, verifica novamente cada nó e
envia somente PATCHs dos campos alterados e POSTs dos volumes ausentes. Confere
o estado persistido ao terminar. Não inicia deploy, restart, exclusão ou limpeza.
A API não oferece transação entre aplicativos; em falha parcial, pare, gere um
novo plano e confira o que foi salvo. Nunca reverta automaticamente dados ou
edições concorrentes.

## Intervenção operacional

A aplicação da configuração e a recuperação dos containers são etapas distintas.
Antes de executá-las, combine uma janela de manutenção com Filippo ou Mario.
Parar os nós interrompe HTTP, sessões realtime e chamadas em andamento. Não
cancele deployments de outra sessão nem implante a branch do PR em produção.

1. Confirme que não há deploy ativo e identifique o SHA atualmente servido e a
   imagem, o container e os mounts de cada aplicativo. Registre esses valores
   junto ao plano. O estado original da STR-27 tinha o principal em `cea013da`
   e as três réplicas em `28d2287a`; reconsulte antes de agir.
2. Preserve uma cópia privada de `/app/uploads` dos quatro containers, inclusive
   os diretórios `.jetstream`, e confirme que os arquivos de backup são legíveis.
   Para obter uma cópia consistente dos arquivos em uso, pare o processo de cada
   nó durante a janela autorizada. Não remova os containers anteriores, seus
   volumes ou suas imagens antes de validar as cópias. Não publique os backups.
   Use o TAR de `docker cp <container>:/app/uploads -` e extraia preservando os
   proprietários numéricos. A cópia direta para um diretório do host pode mapear
   os arquivos para root. Confira hashes, permissões, UID e GID contra a origem
   antes de preparar os volumes; não basta verificar somente os bytes.
   Antes de parar cada processo antigo, leia seu `server_name` na linha inicial
   `INFO` da conexão NATS da porta 4222, usando o endereço interno do container.
   Registre esse nome na cópia correspondente como
   `uploads/.jetstream/.server-name`, com uma linha de texto e permissões 0600,
   mantendo o proprietário do diretório. Confira que o nome salvo corresponde
   ao processo desse nó, nunca a outro peer. Não adivinhe o nome pelo hostname
   do container: a versão antiga acrescentava um sufixo aleatório. O código novo
   recusa estado raft legado sem esse arquivo para não substituir sua identidade.
3. Aplique o plano aprovado. Crie os volumes declarados que ainda não existam no
   Docker e copie para cada volume o conteúdo preservado do nó correspondente,
   mantendo proprietário e permissões. Confira a cópia antes de iniciar o nó.
   Nunca combine árvores JetStream de nós diferentes nem limpe o raft para
   forçar eleição ou provisionamento de buckets.
4. Depois do merge humano e da CI verde, use a revisão que contém a identidade
   persistente. Reimplantar o código antigo ainda geraria nomes novos e não
   resolveria a recuperação de forma durável. Inicie dois nós secundários para
   formar as primeiras rotas.
   Espere ambos passarem `/ready` e confira seus SHAs. Só então avance sobre o
   principal e o quarto nó, um por vez. Use o fluxo normal do Coolify, preservando
   os quality gates. Se os nós não formarem rotas, pare a sequência, consulte DNS,
   aliases e logs e preserve os dados; não force buckets ou saúde positiva.
5. Em cada nó, confirme o alias estável, um único processo por volume e a revisão
   implantada. Aguarde a visão de membros convergir para quatro nós. `/ready`
   precisa retornar 200 no SHA esperado; `/health` 200 isoladamente não basta.
6. No navegador autenticado, envie uma imagem, confirme HTTP 200 e dimensões
   naturais positivas e reabra a task para conferir persistência. Verifique a
   leitura do mesmo asset pelos outros nós e o isolamento entre workspaces.
   Só anexe a captura privada original à STR-23 depois desse sucesso real.

Se a implantação falhar, mantenha os volumes e backups e registre o SHA e o erro
no card e no PR. O container anterior pode voltar a ser iniciado com seu volume
original e a configuração anterior do plano, após revisão humana. Não remova
volumes ou estado JetStream como rollback. Se uma migração de banco tiver sido
executada por uma revisão mais nova, revise a compatibilidade antes de voltar a
uma imagem antiga.

O PR só está pronto depois da validação aplicável e das evidências do seu SHA.
Este procedimento depende do rollout da correção de identidade após o merge
humano. Os aliases, volumes e nomes originais precisam estar preparados antes
desse rollout; o webhook do Coolify pode iniciar o deploy assim que o PR for
mergeado. Combine a janela e prepare os dados antes do merge. Confira também
CI de `main`, deploy e release no SHA correto antes de concluir a STR-27.
