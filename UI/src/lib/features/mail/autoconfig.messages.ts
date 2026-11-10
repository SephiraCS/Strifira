import { getLocale } from '$lib/paraglide/runtime.js';

const translations = {
	en: {
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
