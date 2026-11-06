'use client';

import { useState } from 'react';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import { tomorrow } from 'react-syntax-highlighter/dist/esm/styles/prism';

type Language = 'Python' | 'Go' | 'Java' | 'PHP' | 'JavaScript' | 'Ruby' | 'cURL' | 'C#' | 'Rust';

type Integration = {
  label: string;
  code: string | ((installMethod: number) => string);
  language: string;
  configuration?: string;
};

type InstallationMethod = {
  label: string;
  command: string | ((integrationIndex: number) => string);
  language: string;
};

type LanguageConfig = {
  installation: string | InstallationMethod[];
  installCommand: string;
  integrations: Integration[];
};

const languageConfigs: Record<Language, LanguageConfig> = {
  Python: {
    installation: 'Coming soon',
    installCommand: 'text',
    integrations: [
      {
        label: 'Coming Soon',
        language: 'text',
        code: 'The Python SDK is being updated for the Obzly rebrand.\n\nIn the meantime, use the cURL tab to send logs directly over HTTP.',
      },
    ],
  },
  Go: {
    installation: 'Coming soon',
    installCommand: 'text',
    integrations: [
      {
        label: 'Coming Soon',
        language: 'text',
        code: 'The Go SDK is being updated for the Obzly rebrand.\n\nIn the meantime, use the cURL tab to send logs directly over HTTP.',
      },
    ],
  },
  Java: {
    installation: 'Coming soon',
    installCommand: 'text',
    integrations: [
      {
        label: 'Coming Soon',
        language: 'text',
        code: 'The Java SDK is being updated for the Obzly rebrand.\n\nIn the meantime, use the cURL tab to send logs directly over HTTP.',
      },
    ],
  },
  PHP: {
    installation: 'Coming soon',
    installCommand: 'text',
    integrations: [
      {
        label: 'Coming Soon',
        language: 'text',
        code: 'The PHP SDK is being updated for the Obzly rebrand.\n\nIn the meantime, use the cURL tab to send logs directly over HTTP.',
      },
    ],
  },
  JavaScript: {
    installation: 'Coming soon',
    installCommand: 'text',
    integrations: [
      {
        label: 'Coming Soon',
        language: 'text',
        code: 'The JavaScript/Node SDK is being updated for the Obzly rebrand.\n\nIn the meantime, use the cURL tab to send logs directly over HTTP.',
      },
    ],
  },
  Ruby: {
    installation: 'Coming soon',
    installCommand: 'text',
    integrations: [
      {
        label: 'Coming Soon',
        language: 'text',
        code: 'The Ruby SDK is being updated for the Obzly rebrand.\n\nIn the meantime, use the cURL tab to send logs directly over HTTP.',
      },
    ],
  },
  'C#': {
    installation: 'Coming soon',
    installCommand: 'text',
    integrations: [
      {
        label: 'Coming Soon',
        language: 'text',
        code: 'The C# SDK is being updated for the Obzly rebrand.\n\nIn the meantime, use the cURL tab to send logs directly over HTTP.',
      },
    ],
  },
  Rust: {
    installation: 'Coming soon',
    installCommand: 'text',
    integrations: [
      {
        label: 'Coming Soon',
        language: 'text',
        code: 'Rust integration is coming soon!\n\nStay tuned for updates.',
      },
    ],
  },
  cURL: {
    installation: '',
    installCommand: 'bash',
    integrations: [
      {
        label: 'Basic',
        language: 'bash',
        code: `curl -X POST "http://OBZLY_HOST_PLACEHOLDER/api/v1/logs/receiving/OBZLY_PROJECT_ID_PLACEHOLDER" \\
  -H "Content-Type: application/json" \\
  -H "X-API-Key: OBZLY_API_KEY_PLACEHOLDER" \\
  -d '{
    "logs": [
      {
        "level": "INFO",
        "message": "User logged in successfully",
        "fields": {
          "user_id": "12345",
          "username": "john_doe",
          "ip": "192.168.1.100"
        }
      }
    ]
  }'`,
      },
      {
        label: 'Multiple logs',
        language: 'bash',
        code: `curl -X POST "http://OBZLY_HOST_PLACEHOLDER/api/v1/logs/receiving/OBZLY_PROJECT_ID_PLACEHOLDER" \\
  -H "Content-Type: application/json" \\
  -H "X-API-Key: OBZLY_API_KEY_PLACEHOLDER" \\
  -d '{
    "logs": [
      {
        "level": "INFO",
        "message": "Request received",
        "fields": {
          "path": "/api/users",
          "method": "POST"
        }
      },
      {
        "level": "INFO",
        "message": "Database query executed",
        "fields": {
          "query": "INSERT INTO users",
          "duration_ms": 45
        }
      },
      {
        "level": "INFO",
        "message": "Request completed",
        "fields": {
          "status_code": 201,
          "duration_ms": 120
        }
      }
    ]
  }'`,
      },
    ],
  },
};

const languages: Language[] = [
  'cURL',
  'Python',
  'Go',
  'Java',
  'JavaScript',
  'C#',
  'PHP',
  'Ruby',
  'Rust',
];

const copyToClipboard = async (text: string): Promise<boolean> => {
  if (navigator.clipboard && navigator.clipboard.writeText) {
    try {
      await navigator.clipboard?.writeText(text);
      return true;
    } catch (error) {
      console.warn('Modern clipboard API failed, trying fallback:', error);
    }
  }

  try {
    const textarea = document.createElement('textarea');
    textarea.value = text;
    textarea.style.position = 'fixed';
    textarea.style.left = '-999999px';
    textarea.style.top = '-999999px';
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();

    const successful = document.execCommand('copy');
    document.body.removeChild(textarea);

    if (successful) {
      return true;
    } else {
      throw new Error('execCommand failed');
    }
  } catch (error) {
    console.error('Failed to copy to clipboard:', error);
    return false;
  }
};

interface Props {
  obzlyHost?: string;
  obzlyProjectId?: string;
  obzlyApiKey?: string;
  isApiKeyRequired?: boolean;
}

export function CodeUsageComponent({
  obzlyHost = 'http://OBZLY_HOST',
  obzlyProjectId = 'OBZLY_PROJECT_ID',
  obzlyApiKey = 'OBZLY_API_KEY',
  isApiKeyRequired = false,
}: Props) {
  const [selectedLanguage, setSelectedLanguage] = useState<Language>('cURL');
  const [selectedIntegration, setSelectedIntegration] = useState(0);
  const [selectedInstallMethod, setSelectedInstallMethod] = useState(0);
  const [copiedInstallation, setCopiedInstallation] = useState(false);
  const [copiedConfiguration, setCopiedConfiguration] = useState(false);
  const [copiedUsage, setCopiedUsage] = useState(false);

  const currentConfig = languageConfigs[selectedLanguage];
  const currentIntegration = currentConfig.integrations[selectedIntegration];
  const hasMultipleInstallMethods = Array.isArray(currentConfig.installation);
  const hasConfiguration = currentIntegration.configuration !== undefined;

  const removeApiKeyLines = (code: string): string => {
    const lines = code.split('\n');
    const filteredLines = lines.filter((line) => {
      const lowerLine = line.toLowerCase();
      return !(
        lowerLine.includes('api_key') ||
        lowerLine.includes('apikey') ||
        lowerLine.includes('api-key')
      );
    });
    return filteredLines.join('\n');
  };

  const replacePlaceholders = (text: string): string => {
    let result = text
      .replace(/http:\/\/OBZLY_HOST_PLACEHOLDER/g, obzlyHost)
      .replace(/OBZLY_PROJECT_ID_PLACEHOLDER/g, obzlyProjectId);

    if (isApiKeyRequired) {
      result = result.replace(/OBZLY_API_KEY_PLACEHOLDER/g, obzlyApiKey);
    }

    return result;
  };

  const getRawCode = () => {
    return typeof currentIntegration.code === 'function'
      ? currentIntegration.code(selectedInstallMethod)
      : currentIntegration.code;
  };

  const currentCode = (() => {
    let code = getRawCode();
    code = replacePlaceholders(code);
    if (!isApiKeyRequired) {
      code = removeApiKeyLines(code);
    }
    return code;
  })();

  const getInstallationCommand = () => {
    let command: string;
    if (hasMultipleInstallMethods) {
      const method = (currentConfig.installation as InstallationMethod[])[selectedInstallMethod];
      command =
        typeof method.command === 'function' ? method.command(selectedIntegration) : method.command;
    } else {
      command = currentConfig.installation as string;
    }
    return replacePlaceholders(command);
  };

  const getConfiguration = () => {
    if (!currentIntegration.configuration) return '';
    let config = replacePlaceholders(currentIntegration.configuration);
    if (!isApiKeyRequired) {
      config = removeApiKeyLines(config);
    }
    return config;
  };

  const handleLanguageChange = (language: Language) => {
    setSelectedLanguage(language);
    setSelectedIntegration(0);
    setSelectedInstallMethod(0);
    setCopiedInstallation(false);
    setCopiedConfiguration(false);
    setCopiedUsage(false);
  };

  const handleIntegrationChange = (index: number) => {
    setSelectedIntegration(index);
    setCopiedInstallation(false);
    setCopiedConfiguration(false);
    setCopiedUsage(false);
  };

  const handleInstallMethodChange = (index: number) => {
    setSelectedInstallMethod(index);
    setCopiedInstallation(false);
    setCopiedConfiguration(false);
    setCopiedUsage(false);
  };

  const handleCopyInstallation = async () => {
    const success = await copyToClipboard(getInstallationCommand());
    if (success) {
      setCopiedInstallation(true);
      setTimeout(() => setCopiedInstallation(false), 2000);
    }
  };

  const handleCopyConfiguration = async () => {
    const success = await copyToClipboard(getConfiguration());
    if (success) {
      setCopiedConfiguration(true);
      setTimeout(() => setCopiedConfiguration(false), 2000);
    }
  };

  const handleCopyUsage = async () => {
    const success = await copyToClipboard(currentCode);
    if (success) {
      setCopiedUsage(true);
      setTimeout(() => setCopiedUsage(false), 2000);
    }
  };

  return (
    <div className="mx-auto">
      <div className="mb-4 flex flex-wrap gap-2">
        {languages.map((lang) => (
          <button
            key={lang}
            onClick={() => handleLanguageChange(lang)}
            className={`cursor-pointer rounded-lg px-4 py-2 text-sm font-medium transition-colors ${
              selectedLanguage === lang
                ? 'bg-emerald-600 text-white'
                : 'bg-gray-100 text-gray-700 hover:bg-gray-200 dark:bg-gray-700 dark:text-gray-200'
            }`}
          >
            {lang}
          </button>
        ))}
      </div>

      <div className="mb-4 flex flex-wrap gap-2">
        {currentConfig.integrations.map((integration, index) => (
          <button
            key={index}
            onClick={() => handleIntegrationChange(index)}
            className={`cursor-pointer rounded-lg px-4 py-2 text-sm font-medium transition-colors ${
              selectedIntegration === index
                ? 'bg-emerald-100 text-emerald-700 ring-2 ring-emerald-600'
                : 'bg-gray-50 text-gray-600 hover:bg-gray-100 dark:bg-gray-700 dark:bg-gray-900 dark:text-gray-300 dark:hover:bg-gray-700'
            }`}
          >
            {integration.label}
          </button>
        ))}
      </div>

      {hasMultipleInstallMethods && (
        <div className="mb-4 flex flex-wrap gap-2">
          {(currentConfig.installation as InstallationMethod[]).map((method, index) => (
            <button
              key={index}
              onClick={() => handleInstallMethodChange(index)}
              className={`cursor-pointer rounded-lg px-4 py-2 text-sm font-medium transition-colors ${
                selectedInstallMethod === index
                  ? 'bg-blue-100 text-blue-700 ring-2 ring-blue-600'
                  : 'bg-gray-50 text-gray-600 hover:bg-gray-100 dark:bg-gray-700 dark:bg-gray-900 dark:text-gray-300 dark:hover:bg-gray-700'
              }`}
            >
              {method.label}
            </button>
          ))}
        </div>
      )}

      {getInstallationCommand() && (
        <div className="mb-4 overflow-hidden rounded-lg bg-gray-50 p-4 dark:bg-gray-900">
          <div className="mb-2 text-sm font-semibold text-gray-700 dark:text-gray-200">
            Installation
          </div>
          <div className="relative overflow-auto rounded-lg bg-[#2d2d2d] shadow-lg">
            <button
              onClick={handleCopyInstallation}
              className="absolute top-4 right-4 z-10 cursor-pointer rounded-md bg-gray-700 px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-gray-600"
            >
              {copiedInstallation ? (
                <span className="flex items-center gap-1">
                  <svg className="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M5 13l4 4L19 7"
                    />
                  </svg>
                  Copied!
                </span>
              ) : (
                <span className="flex items-center gap-1">
                  <svg className="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"
                    />
                  </svg>
                  Copy
                </span>
              )}
            </button>

            <div className="m-0 rounded-lg p-6 pr-20 !font-mono text-sm leading-6">
              <SyntaxHighlighter
                language={
                  hasMultipleInstallMethods
                    ? (currentConfig.installation as InstallationMethod[])[selectedInstallMethod]
                        .language
                    : currentConfig.installCommand
                }
                style={tomorrow}
                customStyle={{
                  margin: 0,
                  padding: 0,
                  background: 'transparent',
                }}
                showLineNumbers={false}
              >
                {getInstallationCommand()}
              </SyntaxHighlighter>
            </div>
          </div>
        </div>
      )}

      {hasConfiguration && (
        <div className="mb-4 overflow-hidden rounded-lg bg-gray-50 p-4 dark:bg-gray-900">
          <div className="mb-2 text-sm font-semibold text-gray-700 dark:text-gray-200">
            Configuration
          </div>
          <div className="relative overflow-auto rounded-lg bg-[#2d2d2d] shadow-lg">
            <button
              onClick={handleCopyConfiguration}
              className="absolute top-4 right-4 z-10 cursor-pointer rounded-md bg-gray-700 px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-gray-600"
            >
              {copiedConfiguration ? (
                <span className="flex items-center gap-1">
                  <svg className="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M5 13l4 4L19 7"
                    />
                  </svg>
                  Copied!
                </span>
              ) : (
                <span className="flex items-center gap-1">
                  <svg className="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"
                    />
                  </svg>
                  Copy
                </span>
              )}
            </button>

            <div className="m-0 rounded-lg p-6 pr-20 text-sm leading-6">
              <SyntaxHighlighter
                language={currentIntegration.language}
                style={tomorrow}
                customStyle={{
                  margin: 0,
                  padding: 0,
                  background: 'transparent',
                }}
                showLineNumbers={false}
              >
                {getConfiguration()}
              </SyntaxHighlighter>
            </div>
          </div>
        </div>
      )}

      <div className="mb-4 overflow-hidden rounded-lg bg-gray-50 p-4 dark:bg-gray-900">
        <div className="mb-2 text-sm font-semibold text-gray-700 dark:text-gray-200">Usage</div>
        <div className="relative overflow-auto rounded-lg bg-[#2d2d2d] shadow-lg">
          <button
            onClick={handleCopyUsage}
            className="absolute top-4 right-4 z-10 cursor-pointer rounded-md bg-gray-700 px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-gray-600"
          >
            {copiedUsage ? (
              <span className="flex items-center gap-1">
                <svg className="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M5 13l4 4L19 7"
                  />
                </svg>
                Copied!
              </span>
            ) : (
              <span className="flex items-center gap-1">
                <svg className="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"
                  />
                </svg>
                Copy
              </span>
            )}
          </button>

          <div className="m-0 rounded-lg p-6 pr-20 text-sm leading-6">
            <SyntaxHighlighter
              language={currentIntegration.language}
              style={tomorrow}
              customStyle={{
                margin: 0,
                padding: 0,
                background: 'transparent',
              }}
              showLineNumbers={false}
            >
              {currentCode}
            </SyntaxHighlighter>
          </div>
        </div>
      </div>
    </div>
  );
}
