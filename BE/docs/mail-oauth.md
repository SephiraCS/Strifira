# Autorização de caixas de e-mail

O Strifira descobre os servidores pela conta e oferece OAuth para Google e
Microsoft. Não usa a senha do Strifira como credencial da caixa. A autorização
compartilha a caixa com os membros do workspace, como o cliente de e-mail
existente. O formulário informa isso antes de abrir o consentimento do vendor.

## Cadastro único dos aplicativos

O titular do projeto Google e do tenant Microsoft precisa registrar os
aplicativos e aprovar suas políticas. Não coloque credenciais em cards, PRs,
capturas, variáveis de ambiente ou arquivos do repositório.

Para esta instalação, registre como redirect URI de aplicação **Web**:

`https://strifira.sephira.app.br/mail/oauth/callback`

O backend deriva a URI de `FRONTEND_URL`, já existente no projeto. Não usa o
Host da requisição. O mesmo valor precisa constar no cadastro do vendor. Para
uma validação local autorizada, pode ser registrada também a URI de loopback
do ambiente de teste. Nunca registre domínios de terceiros ou redirects abertos.

### Google

1. No [Google Auth Platform](https://console.cloud.google.com/auth/overview),
   selecione o projeto da empresa. Cadastre branding, contatos e domínios
   autorizados de Strifira. Configure a audiência permitida pela empresa.
2. Crie um cliente OAuth do tipo **Web application**, com a URI acima.
3. Configure o escopo `https://mail.google.com/`. Ele é necessário para
   IMAP/SMTP com XOAUTH2 e é restrito. O Google pode exigir verificação do app,
   avaliação de segurança e aprovação administrativa. Em modo de teste, inclua
   explicitamente a conta de teste na audiência. Tokens de teste podem expirar
   conforme as regras do Google.
4. Guarde o client ID e client secret somente no segredo Vault descrito abaixo.

O fluxo usa code + PKCE S256, `access_type=offline` e `prompt=consent`. O
Strifira exige refresh token para não apresentar uma conexão que deixará de
funcionar ao expirar o primeiro access token. A opção de senha de aplicativo
continua disponível para contas Google cuja política permita esse método.

### Microsoft

1. Em [Microsoft Entra, App registrations](https://entra.microsoft.com/),
   registre uma aplicação com suporte a contas organizacionais de qualquer
   diretório e contas Microsoft pessoais, pois a integração usa o tenant
   `common`. Crie a plataforma **Web**, com a URI acima. Não habilite implicit
   grants nem public client flows.
2. Adicione as permissões **delegadas** de Office 365 Exchange Online
   `IMAP.AccessAsUser.All` e `SMTP.Send`. O fluxo solicita também
   `offline_access`. Não use permissões de aplicação ou acesso global a caixas.
3. Crie um client secret e guarde seu **valor**, junto do Application client ID,
   apenas no Vault. Defina a rotação antes da expiração no processo da empresa.
4. Um administrador pode precisar aprovar o app e permitir IMAP e SMTP AUTH
   OAuth para a caixa. Uma autorização bem-sucedida não substitui essa política:
   o Strifira testa os dois protocolos antes de salvar a conta.

## Entrega automática pelo OCI Vault

O backend usa o SDK oficial OCI com **instance principal**, sem chaves OCI
estáticas. Descobre compartimento e região por IMDSv2 da instância que executa
a API. Se o contêiner não conseguir acessar IMDSv2, a identidade não tiver
permissão ou o segredo estiver ausente/ambíguo, a autorização falha sem criar
conta. Um host fora de OCI não recebe uma credencial fictícia como fallback.

No compartimento da instância, armazene exatamente um segredo ativo para cada
vendor, com estes nomes:

| Provedor | Nome do segredo | Conteúdo JSON |
| --- | --- | --- |
| Google | `strifira-mail-oauth-google` | Campos `client_id` e `client_secret` emitidos pelo Google |
| Microsoft | `strifira-mail-oauth-microsoft` | Campos `client_id` e `client_secret` emitidos pela Microsoft |

Use um Vault da empresa e uma chave de criptografia apropriada. O OCI armazena
o conteúdo como Base64, o que não substitui a criptografia do Vault. A API
consulta a versão **CURRENT** pelo SDK a cada início de autorização/renovação,
sem reinício ou chave de habilitação após a rotação. Segredos com nome duplicado
em Vaults do mesmo compartimento são rejeitados.

Inclua apenas as instâncias da API Strifira no dynamic group. O administrador
OCI deve conceder ao grupo leitura de metadados e bundles dos dois segredos no
compartimento da aplicação. Uma política mínima pode separar:

```text
Allow dynamic-group <grupo-da-api> to inspect secrets in compartment <compartimento-da-api>
Allow dynamic-group <grupo-da-api> to read secret-bundles in compartment <compartimento-da-api>
```

Restrinja `read secret-bundles` aos OCIDs desses dois segredos usando condições
IAM, se o compartimento contiver outros segredos. O SDK não precisa de
permissões para criar/apagar segredos, chaves ou Vaults. A criação do registro
de vendor e seu armazenamento inicial são operações administrativas únicas.
O runtime coleta os valores; ninguém os copia para um contêiner.

Se houver APIs em regiões diferentes, o operador OCI deve disponibilizar os
segredos nas regiões/compartimentos dessas instâncias com a replicação gerida
pelo Vault e a mesma identidade por nome. Este PR não cria infraestrutura OCI,
não transforma hosts externos em instâncias OCI e não altera a entrega legada
da chave de cifragem das contas, derivada do segredo JWT existente.

## Tokens e desconexão

O estado aleatório dura dez minutos, só pode ser usado uma vez e fica ligado ao
usuário/workspace e ao verificador PKCE cifrado no PostgreSQL. O retorno exige
a sessão Strifira e a participação atual no workspace. A página remove código
e estado da URL antes de chamar a API. Recusa ou falha de conexão não cria
conta nem modifica uma conta já vinculada.

Access e refresh tokens ficam cifrados com AES-GCM, fora dos DTOs públicos.
A renovação é serializada por locks PostgreSQL entre processos. O token
rotacionado é persistido antes do uso. `invalid_grant` elimina a credencial
local e pede nova autorização. Reconexões preservam o histórico da caixa e
rejeitam autorizações iniciadas antes de uma edição concorrente da conta.

Na desconexão Google, a API revoga o grant no endpoint publicado pelo Google
antes de apagar a conta e os tokens locais. Se a revogação falhar, mantém a
conta e informa o erro para permitir nova tentativa. O Google pode revogar o
grant inteiro do mesmo aplicativo/usuário; outras conexões desse mesmo grant
podem exigir novo consentimento.

Microsoft não publica endpoint RFC 7009 para revogação individual desse grant.
A desconexão destrói os tokens locais. Para remover também o consentimento,
o titular usa [conta pessoal](https://account.live.com/consent/Manage) ou
[conta de trabalho](https://myapps.microsoft.com). O Strifira não tenta revogar
todas as sessões do usuário via permissões administrativas Graph.

## Validação com um titular autorizado

Depois de cadastrar os aplicativos, entregar os segredos e executar este commit
em uma instância OCI autorizada, o titular deve:

1. Entrar no Strifira e selecionar o workspace que receberá acesso à caixa.
2. Adicionar a conta, conferir endereço e aviso de compartilhamento, clicar em
   **Autorizar conta** e consentir no provedor. Não compartilhar a senha da caixa.
3. Confirmar o retorno ao e-mail e a ausência de erro de sincronização.
4. Enviar uma mensagem de teste para um destinatário próprio autorizado,
   responder por esse destinatário e conferir a chegada da resposta na INBOX
   do Strifira. Não usar terceiros não envolvidos no teste.
5. Confirmar uma renovação após a expiração natural do access token e testar
   desconexão/reautorização. Remover o consentimento no vendor e confirmar que
   o Strifira pede nova autorização, sem expor tokens.

Registre no card apenas provedor, SHA executado, horário, operações e resultado,
com dados pessoais ocultos nas mídias. Testes com endpoints OAuth e servidores
IMAP/SMTP locais não comprovam funcionamento com Google/Microsoft. O cadastro,
a política da caixa e o consentimento do titular não podem ser substituídos
por fixtures, senhas do Strifira ou um resultado de CI.
