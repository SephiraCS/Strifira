import { getLocale } from '$lib/paraglide/runtime.js';

const translations = {
	en: {
		authorize: 'Authorize account',
		reauthorize: 'Authorize again',
		oauth_consent:
			'You will authorize access at your provider. This mailbox will be shared with workspace members. Strifira does not receive your provider password.',
		oauth_failed: 'Authorization was not completed. No account was connected or changed. Try again from Mail.',
		back_to_mail: 'Back to Mail',
		use_app_password: 'Use an app password instead',
		microsoft_revoke:
			'Disconnecting removes tokens from Strifira. To revoke Microsoft consent as well, use your personal or work account portal.',
		manage_consent: 'Personal account',
		disconnect:
			'Disconnecting removes this mailbox and its local tokens. Google authorization is also revoked with the provider. For Microsoft, remove consent in the account portal if desired.',
		description:
			'Enter your e-mail address. Strifira discovers the provider and secure connection settings automatically.',
		found: '{provider} identified',
		secure: 'Incoming and outgoing mail will use encrypted connections.',
		oauth_required:
			'This provider requires OAuth authorization. Strifira must first be registered with the provider. No account has been connected.',
		app_password:
			'Create an app password with your provider. Your provider may require two-step verification or administrator approval.',
		provider_help: 'Create an app password with this provider',
		password_label: 'App password',
		credential_hint:
			'Use the credential for this mailbox, which may differ from your Strifira password. Credentials are encrypted and shared mailboxes are accessible to workspace members.',
		discovering: 'Finding provider…',
		validating: 'Validating incoming and outgoing mail…',
		retry: 'Try again',
		continue: 'Continue',
		connect: 'Connect account'
	},
	'pt-BR': {
		authorize: 'Autorizar conta',
		reauthorize: 'Autorizar novamente',
		oauth_consent:
			'Você autorizará o acesso no provedor. Esta caixa será compartilhada com os membros do workspace. O Strifira não recebe a senha do provedor.',
		oauth_failed:
			'A autorização não foi concluída. Nenhuma conta foi conectada ou alterada. Tente novamente em E-mail.',
		back_to_mail: 'Voltar ao e-mail',
		use_app_password: 'Usar senha de aplicativo',
		microsoft_revoke:
			'A desconexão apaga os tokens no Strifira. Para também remover o consentimento Microsoft, use o portal da conta pessoal ou de trabalho.',
		manage_consent: 'Conta pessoal',
		disconnect:
			'A desconexão remove esta caixa e seus tokens locais. A autorização Google também é revogada no provedor. Para Microsoft, remova o consentimento no portal da conta se desejar.',
		description:
			'Informe seu e-mail. O Strifira descobre o provedor e os parâmetros de conexão segura automaticamente.',
		found: '{provider} identificado',
		secure: 'Recebimento e envio usarão conexões criptografadas.',
		oauth_required:
			'Este provedor exige autorização OAuth. O Strifira precisa primeiro ser registrado no provedor. Nenhuma conta foi conectada.',
		app_password:
			'Crie uma senha de aplicativo no provedor. Ele pode exigir verificação em duas etapas ou aprovação do administrador.',
		provider_help: 'Criar senha de aplicativo neste provedor',
		password_label: 'Senha de aplicativo',
		credential_hint:
			'Use a credencial desta caixa, que pode ser diferente da senha do Strifira. As credenciais ficam criptografadas e a caixa é compartilhada com os membros do workspace.',
		discovering: 'Descobrindo provedor…',
		validating: 'Validando recebimento e envio…',
		retry: 'Tentar novamente',
		continue: 'Continuar',
		connect: 'Conectar conta'
	},
	it: {
		authorize: 'Autorizza account',
		reauthorize: 'Autorizza di nuovo',
		oauth_consent:
			'Autorizzerai l’accesso presso il provider. La casella sarà condivisa con i membri del workspace. Strifira non riceve la password del provider.',
		oauth_failed: 'Autorizzazione non completata. Nessun account è stato collegato o modificato. Riprova dalla posta.',
		back_to_mail: 'Torna alla posta',
		use_app_password: 'Usa una password per app',
		microsoft_revoke:
			'La disconnessione elimina i token da Strifira. Per revocare anche il consenso Microsoft, usa il portale dell’account personale o di lavoro.',
		manage_consent: 'Account personale',
		disconnect:
			'La disconnessione rimuove la casella e i token locali. L’autorizzazione Google viene anche revocata presso il provider. Per Microsoft, rimuovi il consenso nel portale dell’account se lo desideri.',
		description:
			'Inserisci il tuo indirizzo e-mail. Strifira rileva automaticamente il provider e i parametri di connessione sicura.',
		found: '{provider} identificato',
		secure: 'La ricezione e l’invio useranno connessioni crittografate.',
		oauth_required:
			'Questo provider richiede l’autorizzazione OAuth. Strifira deve prima essere registrato presso il provider. Nessun account è stato collegato.',
		app_password:
			'Crea una password per app presso il provider. Potrebbero essere richieste la verifica in due passaggi o l’approvazione dell’amministratore.',
		provider_help: 'Crea una password per app presso questo provider',
		password_label: 'Password per app',
		credential_hint:
			'Usa la credenziale di questa casella, che può essere diversa dalla password di Strifira. Le credenziali sono crittografate e la casella è condivisa con i membri del workspace.',
		discovering: 'Ricerca del provider…',
		validating: 'Verifica della ricezione e dell’invio…',
		retry: 'Riprova',
		continue: 'Continua',
		connect: 'Collega account'
	},
	'zh-CN': {
		authorize: '授权帐户',
		reauthorize: '重新授权',
		oauth_consent: '您将在提供商处授权访问。此邮箱将与工作区成员共享。Strifira 不会收到提供商密码。',
		oauth_failed: '授权未完成。未连接或更改任何帐户。请从邮件页面重试。',
		back_to_mail: '返回邮件',
		use_app_password: '改用应用专用密码',
		microsoft_revoke: '断开连接会删除 Strifira 中的令牌。要撤销 Microsoft 授权，请访问个人或工作帐户门户。',
		manage_consent: '个人帐户',
		disconnect: '断开连接会移除此邮箱及其本地令牌，并撤销 Google 授权。对于 Microsoft，可在帐户门户中移除授权。',
		description: '输入电子邮件地址。Strifira 会自动发现提供商和安全连接设置。',
		found: '已识别 {provider}',
		secure: '收取和发送邮件均使用加密连接。',
		oauth_required: '此提供商需要 OAuth 授权。必须先向提供商注册 Strifira。目前尚未连接任何帐户。',
		app_password: '请在提供商处创建应用专用密码。提供商可能要求两步验证或管理员批准。',
		provider_help: '在此提供商处创建应用专用密码',
		password_label: '应用专用密码',
		credential_hint: '使用此邮箱的凭据，它可能与 Strifira 密码不同。凭据以加密方式保存，工作区成员可以访问共享邮箱。',
		discovering: '正在查找提供商…',
		validating: '正在验证收发邮件…',
		retry: '重试',
		continue: '继续',
		connect: '连接帐户'
	},
	'zh-TW': {
		authorize: '授權帳戶',
		reauthorize: '重新授權',
		oauth_consent: '您將在供應商處授權存取。此信箱將與工作區成員共用。Strifira 不會收到供應商密碼。',
		oauth_failed: '授權未完成。未連接或變更任何帳戶。請從郵件頁面重試。',
		back_to_mail: '返回郵件',
		use_app_password: '改用應用程式專用密碼',
		microsoft_revoke: '中斷連線會刪除 Strifira 中的權杖。要撤銷 Microsoft 授權，請前往個人或工作帳戶入口網站。',
		manage_consent: '個人帳戶',
		disconnect: '中斷連線會移除此信箱及其本機權杖，並撤銷 Google 授權。對於 Microsoft，可在帳戶入口網站中移除授權。',
		description: '輸入電子郵件地址。Strifira 會自動探索供應商及安全連線設定。',
		found: '已識別 {provider}',
		secure: '收取和傳送郵件均使用加密連線。',
		oauth_required: '此供應商需要 OAuth 授權。必須先向供應商註冊 Strifira。目前尚未連接任何帳戶。',
		app_password: '請在供應商處建立應用程式專用密碼。供應商可能要求兩步驟驗證或管理員核准。',
		provider_help: '在此供應商處建立應用程式專用密碼',
		password_label: '應用程式專用密碼',
		credential_hint: '使用此信箱的憑證，它可能與 Strifira 密碼不同。憑證以加密方式儲存，工作區成員可以存取共用信箱。',
		discovering: '正在尋找供應商…',
		validating: '正在驗證郵件收發…',
		retry: '重試',
		continue: '繼續',
		connect: '連接帳戶'
	}
} as const;

export function mailAccountMessage(key: keyof typeof translations.en, provider = ''): string {
	return translations[getLocale()][key].replace('{provider}', provider);
}
