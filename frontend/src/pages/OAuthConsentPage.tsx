import { LoadingOutlined } from '@ant-design/icons';
import { Alert, Button, Spin } from 'antd';
import { type JSX, useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router';

import { type OAuthConsentInfo, oauthApi, userApi } from '../entity/users';
import { consumeReturnTo, rememberReturnTo } from '../shared/lib/returnTo';
import { AuthPageComponent } from './AuthPageComponent';

const PARAMS = [
  'response_type',
  'client_id',
  'redirect_uri',
  'scope',
  'state',
  'code_challenge',
  'code_challenge_method',
  'resource',
];

interface Props {
  isAuthorized: boolean;
}

/** OAuth 2.1 consent screen for MCP clients (claude.ai, Claude Desktop, ...). */
export const OAuthConsentPage = ({ isAuthorized }: Props): JSX.Element => {
  const [searchParams] = useSearchParams();
  const [info, setInfo] = useState<OAuthConsentInfo | null>(null);
  const [email, setEmail] = useState('');
  const [error, setError] = useState('');
  const [isSubmitting, setSubmitting] = useState(false);

  const params = useMemo(() => {
    const collected: Record<string, string> = {};
    for (const name of PARAMS) {
      const value = searchParams.get(name);
      if (value !== null) collected[name] = value;
    }
    return collected;
  }, [searchParams]);

  const decide = async (approve: boolean) => {
    setSubmitting(true);
    try {
      window.location.assign(await oauthApi.decide(params, approve));
    } catch (e) {
      setError((e as Error).message);
      setSubmitting(false);
    }
  };

  useEffect(() => {
    if (!isAuthorized) {
      rememberReturnTo(window.location.pathname + window.location.search);
      return;
    }
    consumeReturnTo();
    oauthApi
      .validate(params)
      .then((result) => {
        if (result.ok) {
          setInfo(result.info);
        } else if (result.redirectTo) {
          window.location.assign(result.redirectTo);
        } else {
          setError(result.error);
        }
      })
      .catch(() => setError('Could not check this authorization request'));
    userApi
      .getCurrentUser()
      .then((user) => setEmail(user.email))
      .catch(() => setEmail(''));
  }, [isAuthorized, params]);

  if (!isAuthorized) {
    return <AuthPageComponent />;
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-gray-50 p-4 dark:bg-gray-900">
      <div className="w-full max-w-md rounded-lg bg-white p-6 shadow dark:bg-gray-800">
        {error ? (
          <>
            <div className="mb-3 text-lg font-semibold">Can&apos;t connect this app</div>
            <Alert type="error" showIcon message={error} />
          </>
        ) : !info ? (
          <div className="flex justify-center py-8">
            <Spin indicator={<LoadingOutlined spin />} />
          </div>
        ) : (
          <>
            <div className="mb-1 text-xl font-semibold">
              Allow {info.clientName} to access Obzly?
            </div>
            {email && (
              <div className="mb-4 text-sm text-gray-500 dark:text-gray-300">
                Signed in as {email}
              </div>
            )}

            <div className="mb-4 text-sm text-gray-700 dark:text-gray-200">
              {info.clientName} will get <b>read-only</b> access to the logs, traces, metrics and
              dashboards your account can see, through MCP. It can&apos;t change anything.
            </div>

            {!info.verifiedIdentity && (
              <Alert
                className="mb-4"
                type="warning"
                showIcon
                message={`This app registered itself. Only allow it if you started this from ${info.redirectHost}.`}
              />
            )}

            <div className="mb-5 text-xs text-gray-500 dark:text-gray-400">
              You&apos;ll return to <b>{info.redirectHost}</b>. You can revoke access anytime under
              Profile, AI assistant access.
            </div>

            <div className="flex justify-end gap-2">
              <Button disabled={isSubmitting} onClick={() => decide(false)}>
                Deny
              </Button>
              <Button type="primary" loading={isSubmitting} onClick={() => decide(true)}>
                Allow
              </Button>
            </div>
          </>
        )}
      </div>
    </div>
  );
};
