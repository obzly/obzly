import { App as AntdApp, ConfigProvider, theme as antdTheme } from 'antd';
import { useEffect, useState } from 'react';
import { BrowserRouter, Route } from 'react-router';
import { Routes } from 'react-router';

import { mfaApi, userApi } from './entity/users';
import { MFAForcedSetupComponent } from './features/users/ui/MFAForcedSetupComponent';
import { AuthPageComponent } from './pages/AuthPageComponent';
import { OAuthCallbackPage } from './pages/OAuthCallbackPage';
import { OAuthConsentPage } from './pages/OAuthConsentPage';
import { SiteProvider } from './shared/site';
import { ThemeProvider, useTheme } from './shared/theme';
import { MainScreenComponent } from './widgets/main';

function AppContent() {
  const [isAuthorized, setIsAuthorized] = useState(false);
  // Checked here, not only at sign-in: an older token or a just-promoted admin
  // never goes through the sign-in MFA flow. undefined = not checked yet.
  const [mfaSetupRequired, setMfaSetupRequired] = useState<boolean | undefined>(undefined);

  const checkMfaGate = () => {
    if (!userApi.isAuthorized()) {
      setMfaSetupRequired(undefined);
      return;
    }

    mfaApi
      .getStatus()
      .then((status) =>
        setMfaSetupRequired(
          status.required && !status.enabled && !status.satisfiedByIdentityProvider,
        ),
      )
      .catch(() => setMfaSetupRequired(false));
  };

  useEffect(() => {
    const isAuthorized = userApi.isAuthorized();
    setIsAuthorized(isAuthorized);
    if (isAuthorized) {
      checkMfaGate();
    }

    userApi.addAuthListener(() => {
      const authorized = userApi.isAuthorized();
      setIsAuthorized(authorized);
      if (authorized) {
        checkMfaGate();
      } else {
        setMfaSetupRequired(undefined);
      }
    });
  }, []);

  const showMfaGate = isAuthorized && mfaSetupRequired === true;
  const { theme } = useTheme();

  return (
    <ConfigProvider
      theme={{
        token: {
          colorPrimary: '#009966', // Tailwind emerald-600
        },
        algorithm: theme === 'dark' ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
      }}
    >
      {/* One failing backend can make many components error at once; cap the stack. */}
      <AntdApp message={{ maxCount: 3 }}>
        {showMfaGate ? (
          <MFAForcedSetupComponent onEnabled={() => setMfaSetupRequired(false)} />
        ) : (
          <BrowserRouter>
            <Routes>
              <Route path="/auth/callback" element={<OAuthCallbackPage />} />
              <Route
                path="/oauth/authorize"
                element={<OAuthConsentPage isAuthorized={isAuthorized} />}
              />
              <Route
                path="/"
                element={
                  !isAuthorized ? (
                    <AuthPageComponent />
                  ) : (
                    <SiteProvider>
                      <MainScreenComponent />
                    </SiteProvider>
                  )
                }
              />
            </Routes>
          </BrowserRouter>
        )}
      </AntdApp>
    </ConfigProvider>
  );
}

function App() {
  return (
    <ThemeProvider>
      <AppContent />
    </ThemeProvider>
  );
}

export default App;
