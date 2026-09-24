import React, { useEffect, useState } from 'react';

import { Container, Segment } from 'semantic-ui-react';
import { getFooterHTML, getSystemName } from '../helpers';

const Footer = () => {
  const systemName = getSystemName();
  const [footer, setFooter] = useState(getFooterHTML());
  let remainCheckTimes = 5;

  const loadFooter = () => {
    let footer_html = localStorage.getItem('footer_html');
    if (footer_html) {
      setFooter(footer_html);
    }
  };

  useEffect(() => {
    const timer = setInterval(() => {
      if (remainCheckTimes <= 0) {
        clearInterval(timer);
        return;
      }
      remainCheckTimes--;
      loadFooter();
    }, 200);
    return () => clearTimeout(timer);
  }, []);

  return (
    <Segment vertical>
      <Container textAlign='center'>
        {footer ? (
          <div
            className='custom-footer'
            dangerouslySetInnerHTML={{ __html: footer }}
          ></div>
        ) : (
          <div className='custom-footer'>
            <a
              href='https://github.com/infinmalum/one-gateway'
              target='_blank'
            >
              {systemName} {import.meta.env.REACT_APP_VERSION}{' '}
            </a>
            基于{' '}
            <a href='https://github.com/songquanpeng/one-api' target='_blank'>
              One API（JustSong）
            </a>{' '}
            ，主题 air 来自{' '}
            <a href='https://github.com/Calcium-Ion' target='_blank'>
              Calon
            </a>{' '}；原项目采用 MIT，派生修改采用{' '}
            <a href='https://github.com/infinmalum/one-gateway/blob/main/LICENSE'>
              Apache 2.0
            </a>
          </div>
        )}
      </Container>
    </Segment>
  );
};

export default Footer;
