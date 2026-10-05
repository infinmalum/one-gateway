import React, { useContext, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card, Grid, Header } from 'semantic-ui-react';
import { API, showError, showNotice, timestamp2string } from '../../helpers';
import { StatusContext } from '../../context/Status';
import { marked } from 'marked';
import { UserContext } from '../../context/User';
import { Link } from 'react-router-dom';

const StatusRow = ({
  icon,
  label,
  children,
}: {
  icon: string;
  label: string;
  children: React.ReactNode;
}) => (
  <p
    style={{
      display: 'flex',
      alignItems: 'center',
      gap: '0.5em',
      margin: '0.6em 0',
    }}
  >
    <i className={`${icon} icon`}></i>
    <span style={{ fontWeight: 'bold' }}>{label}</span>
    {children}
  </p>
);

const Home = () => {
  const { t } = useTranslation();
  const [statusState, statusDispatch] = useContext(StatusContext);
  const [homePageContentLoaded, setHomePageContentLoaded] = useState(false);
  const [homePageContent, setHomePageContent] = useState('');
  const [userState] = useContext(UserContext);

  const displayNotice = async () => {
    const res = await API.get('/api/notice');
    const { success, message, data } = res.data;
    if (success) {
      let oldNotice = localStorage.getItem('notice');
      if (data !== oldNotice && data !== '') {
        const htmlNotice = marked(data);
        showNotice(htmlNotice, true);
        localStorage.setItem('notice', data);
      }
    } else {
      showError(message);
    }
  };

  const displayHomePageContent = async () => {
    setHomePageContent(localStorage.getItem('home_page_content') || '');
    const res = await API.get('/api/home_page_content');
    const { success, message, data } = res.data;
    if (success) {
      let content = data;
      if (!data.startsWith('https://')) {
        content = marked.parse(data);
      }
      setHomePageContent(content);
      localStorage.setItem('home_page_content', content);
    } else {
      showError(message);
      setHomePageContent(t('home.loading_failed'));
    }
    setHomePageContentLoaded(true);
  };

  const getStartTimeString = () => {
    const timestamp = statusState?.status?.start_time;
    return timestamp2string(timestamp);
  };

  useEffect(() => {
    displayNotice().then();
    displayHomePageContent().then();
  }, []);

  return (
    <>
      {homePageContentLoaded && homePageContent === '' ? (
        <div className='dashboard-container'>
          <Card fluid className='chart-card'>
            <Card.Content>
              <Card.Header className='header'>
                {t('home.welcome.title')}
              </Card.Header>
              <Card.Description style={{ lineHeight: '1.6' }}>
                <p>{t('home.welcome.description')}</p>
                {!userState.user && <p>{t('home.welcome.login_notice')}</p>}
              </Card.Description>
            </Card.Content>
          </Card>
          <Card fluid className='chart-card'>
            <Card.Content>
              <Card.Header>
                <Header as='h3'>{t('home.system_status.title')}</Header>
              </Card.Header>
              <Grid columns={2} stackable>
                <Grid.Column>
                  <Card
                    fluid
                    className='chart-card'
                    style={{ boxShadow: '0 1px 3px rgba(0,0,0,0.12)' }}
                  >
                    <Card.Content>
                      <Card.Header>
                        <Header as='h3' style={{ color: '#444' }}>
                          {t('home.system_status.info.title')}
                        </Header>
                      </Card.Header>
                      <Card.Description
                        style={{ lineHeight: '2', marginTop: '1em' }}
                      >
                        <StatusRow
                          icon='info circle'
                          label={t('home.system_status.info.name')}
                        >
                          <span>{statusState?.status?.system_name}</span>
                        </StatusRow>
                        <StatusRow
                          icon='code branch'
                          label={t('home.system_status.info.version')}
                        >
                          <span>
                            {statusState?.status?.version || 'unknown'}
                          </span>
                        </StatusRow>
                        <StatusRow
                          icon='github'
                          label={t('home.system_status.info.source')}
                        >
                          <a
                            href='https://github.com/songquanpeng/one-api'
                            target='_blank'
                            style={{ color: '#2185d0' }}
                          >
                            {t('home.system_status.info.source_link')}
                          </a>
                        </StatusRow>
                        <StatusRow
                          icon='clock outline'
                          label={t('home.system_status.info.start_time')}
                        >
                          <span>{getStartTimeString()}</span>
                        </StatusRow>
                      </Card.Description>
                    </Card.Content>
                  </Card>
                </Grid.Column>

                <Grid.Column>
                  <Card
                    fluid
                    className='chart-card'
                    style={{ boxShadow: '0 1px 3px rgba(0,0,0,0.12)' }}
                  >
                    <Card.Content>
                      <Card.Header>
                        <Header as='h3' style={{ color: '#444' }}>
                          {t('home.system_status.config.title')}
                        </Header>
                      </Card.Header>
                      <Card.Description
                        style={{ lineHeight: '2', marginTop: '1em' }}
                      >
                        {[
                          {
                            icon: 'envelope',
                            label: t('home.system_status.config.email_verify'),
                            enabled: statusState?.status?.email_verification,
                          },
                          {
                            icon: 'github',
                            label: t('home.system_status.config.github_oauth'),
                            enabled: statusState?.status?.github_oauth,
                          },
                          {
                            icon: 'wechat',
                            label: t('home.system_status.config.wechat_login'),
                            enabled: statusState?.status?.wechat_login,
                          },
                          {
                            icon: 'shield alternate',
                            label: t('home.system_status.config.turnstile'),
                            enabled: statusState?.status?.turnstile_check,
                          },
                        ].map((item) => (
                          <StatusRow
                            key={item.icon}
                            icon={item.icon}
                            label={item.label}
                          >
                            <span
                              style={{
                                color: item.enabled ? '#21ba45' : '#db2828',
                                fontWeight: 500,
                              }}
                            >
                              {item.enabled
                                ? t('home.system_status.config.enabled')
                                : t('home.system_status.config.disabled')}
                            </span>
                          </StatusRow>
                        ))}
                      </Card.Description>
                    </Card.Content>
                  </Card>
                </Grid.Column>
              </Grid>
            </Card.Content>
          </Card>
        </div>
      ) : (
        <>
          {homePageContent.startsWith('https://') ? (
            <iframe
              src={homePageContent}
              style={{ width: '100%', height: '100vh', border: 'none' }}
            />
          ) : (
            <div
              className='home-markdown'
              dangerouslySetInnerHTML={{ __html: homePageContent }}
            ></div>
          )}
        </>
      )}
    </>
  );
};

export default Home;
