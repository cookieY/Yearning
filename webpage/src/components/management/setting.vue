<style>
  label {
    font-size: 30px;
  }
</style>

<template>
  <div>
    <Row>
      <Col span="24">
        <Card>
          <p slot="title">
            <Icon type="md-settings"></Icon>
            基础设置
          </p>
          <Row>
            <Col span="12">
              <Card>
                <p slot="title">Inception配置</p>
                <Form :label-width="120">
                  <FormItem label="地址:">
                    <Input placeholder="Inception ip地址" v-model="inception.host"></Input>
                  </FormItem>
                  <FormItem label="端口:">
                    <Input placeholder="Inception 端口" v-model="inception.port"></Input>
                  </FormItem>
                  <FormItem label="用户名:">
                    <Input placeholder="Inception 用户名" v-model="inception.user"></Input>
                  </FormItem>
                  <FormItem label="密码:">
                    <Input placeholder="Inception 密码(如未设置密码则不填写)" type="password" v-model="inception.password"></Input>
                  </FormItem>
                  <FormItem label="备份库地址:">
                    <Input placeholder="备份库 地址" v-model="inception.back_host"></Input>
                  </FormItem>
                  <FormItem label="备份库端口:">
                    <Input placeholder="备份库 端口" v-model="inception.back_port"></Input>
                  </FormItem>
                  <FormItem label="备份库用户名:">
                    <Input placeholder="备份库 用户名" v-model="inception.back_user"></Input>
                  </FormItem>
                  <FormItem label="备份库密码:">
                    <Input placeholder="备份库 密码(如未设置密码则不填写)" type="password" v-model="inception.back_password"></Input>
                  </FormItem>
                </Form>
              </Card>
            </Col>
            <Col span="12">
              <Card style="margin-left: 5%">
                <p slot="title">LDAP设置</p>
                <Form :label-width="120">
                  <FormItem label="LDAP认证类型:">
                    <Select v-model="ldap.type">
                      <Option value="1">域名认证</Option>
                      <Option value="2">uid认证</Option>
                      <Option value="3">cn认证</Option>
                    </Select>
                  </FormItem>
                  <FormItem label="服务地址:">
                    <Input placeholder="服务ip地址" v-model="ldap.host"></Input>
                  </FormItem>
                  <FormItem label="LDAP_SCBASE:">
                    <Input placeholder="LDAP dc 相关设置,采用域名认证可不填写" v-model="ldap.sc"></Input>
                  </FormItem>
                  <FormItem >
                    <Checkbox v-model="ldap.ou">启用多ou</Checkbox>
                  </FormItem>
                  <FormItem label="LDAP_域名:">
                    <Input placeholder="LDAP Domain" v-model="ldap.domain"></Input>
                  </FormItem>
                  <FormItem label="LDAP_测试用户:">
                    <Input placeholder="请填写测试用户" v-model="ldap.user"></Input>
                  </FormItem>
                  <FormItem label="LDAP_测试密码:">
                    <Input placeholder="请填写测试密码" v-model="ldap.password" type="password"></Input>
                  </FormItem>
                  <Button type="primary" @click="ldap_test()">ldap测试</Button>
                </Form>
              </Card>
              <br>
              <Alert style="margin-left: 5%" type="warning" show-icon>
                注意事项：
                <template slot="desc">
                  1.请确认已正确修改或替换pymysql相关模块。否则inception将无法正常使用！
                  <br>
                  2.请正确填写Inception备份库相关信息，否则将无法获得回滚语句。(无法获得回滚语句的原因有多种这只是其中之一)
                  <br>
                  3.LDAP登陆建议使用域名方式登陆，如使用其他的方式配置较为繁琐。比如使用uid方式需在LDAP_SCBASE中填写相关dc，cn等相关信息
                </template>
              </Alert>
            </Col>
          </Row>
        </Card>
      </Col>
    </Row>
    <br>
    <Row>
      <Col span="24">
        <Card>
          <p slot="title">
            <Icon type="android-settings"></Icon>
            进阶设置
          </p>
          <Row>
            <Col span="12">
              <Card>
                <p slot="title">消息推送</p>
                <Form :label-width="120">
                  <FormItem label="钉钉webhook:">
                    <Input placeholder="此webhook只用于查询工单,权限工单的消息推送。" v-model="message.webhook"></Input>
                  </FormItem>
                  <FormItem label="邮件SMTP服务地址:">
                    <Input placeholder="STMP服务 地址" v-model="message.smtp_host"></Input>
                  </FormItem>
                  <FormItem >
                    <Checkbox v-model="message.ssl">启用ssl端口</Checkbox>
                  </FormItem>
                  <FormItem label="SMTP服务端口:">
                    <Input placeholder="STMP服务 端口" v-model="message.smtp_port"></Input>
                  </FormItem>
                  <FormItem label="邮件推送人用户名:">
                    <Input placeholder="推送人 用户名" v-model="message.user"></Input>
                  </FormItem>
                  <FormItem label="邮件推送人密码:">
                    <Input placeholder="推送人 密码" type="password" v-model="message.password"></Input>
                  </FormItem>
                  <FormItem label="邮件测试收件地址::">
                    <Input placeholder="测试收件人地址填写" v-model="message.to_user"></Input>
                  </FormItem>
                  <Form-item label="email推送开关:">
                    <i-switch v-model="message.mail" size="large" @on-change="mail_switching">
                      <span slot="open">开</span>
                      <span slot="close">关</span>
                    </i-switch>
                  </Form-item>
                  <Form-item label="钉钉推送开关:">
                    <i-switch v-model="message.ding" size="large" @on-change="dingding_switching">
                      <span slot="open">开</span>
                      <span slot="close">关</span>
                    </i-switch>
                  </Form-item>
                  <Button type="primary" @click="dingding_test()">钉钉测试</Button>
                  <Button type="warning" @click="mail_test()" style="margin-left: 5%">邮件测试</Button>
                </Form>
              </Card>
            </Col>
            <Col span="12">
              <Card style="margin-left: 5%">
                <p slot="title">其他</p>
                <Form :label-width="120">
                  <FormItem label="查询最大Limit限制:">
                    <Input placeholder="查询最大的Limit数。" v-model="other.limit"></Input>
                  </FormItem>
                  <FormItem label="自定义机房:">
                    <Tag v-for="item in other.con_room" :key="item" :name="item" type="border" closable color="blue"
                         @on-close="handleClose2">{{ item }}
                    </Tag>
                    <br>
                    <Input placeholder="机房名称" v-model="other.foce" style="width: 30%"></Input>
                    <Button icon="ios-plus-empty" type="dashed" size="small" @click="handleAdd">添加机房</Button>
                  </FormItem>
                  <FormItem label="排除数据库:">
                    <Tag v-for="v in other.exclued_db_list" :key="v" :name="v" type="border" closable color="blue"
                         @on-close="handleClose_exclued_db">{{ v }}
                    </Tag>
                    <br>
                    <Input placeholder="排除数据库" v-model="other.exclued_db" style="width: 30%"></Input>
                    <Button icon="ios-plus-empty" type="dashed" size="small" @click="handleAdd_exclued_db">添加排除数据库</Button>
                  </FormItem>
                  <FormItem label="可注册邮箱后缀:">
                    <Tag v-for="v in other.email_suffix_list" :key="v" :name="v" type="border" closable color="blue"
                         @on-close="handleCloseemail">{{ v }}
                    </Tag>
                    <br>
                    <Input placeholder="可注册邮箱后缀" v-model="other.email_suffix" style="width: 30%"></Input>
                    <Button icon="ios-plus-empty" type="dashed" size="small" @click="handleAddemail">添加邮箱后缀</Button>
                  </FormItem>
                  <FormItem label="脱敏字段:">
                    <Tag v-for="v in other.sensitive_list" :key="v" :name="v" type="border" closable color="blue"
                         @on-close="handleClose3">{{ v }}
                    </Tag>
                    <br>
                    <Input placeholder="脱敏字段设置" v-model="other.sensitive" style="width: 30%"></Input>
                    <Button icon="ios-plus-empty" type="dashed" size="small" @click="handleAdd1">添加脱敏字段</Button>
                  </FormItem>
                  <Form-item label="多级审核开关:">
                    <i-switch size="large" @on-change="multi_switching" v-model="other.multi">
                      <span slot="open">开</span>
                      <span slot="close">关</span>
                    </i-switch>
                  </Form-item>
                  <Form-item label="查询审核开关:">
                    <i-switch size="large" @on-change="multi_query" v-model="other.query">
                      <span slot="open">开</span>
                      <span slot="close">关</span>
                    </i-switch>
                  </Form-item>
                </Form>
              </Card>
              <br>
              <Alert style="margin-left: 5%" type="warning" show-icon>
                注意事项：
                <template slot="desc">
                  1.此处设置的钉钉webhook并不会对SQL工单进行消息推送，如需对SQL工单进行消息推送请前往数据库管理页面设置
                  <br>
                  2.由于各个邮件服务提供商对于垃圾邮件过滤的机制各不相同，可能会造成邮件无法接收的情况。所以使用前请测试是否稳定,推荐使用搜狐
                  <br>
                  3.只有开启相应的消息推送开关后，消息推送才会开启。否则只有站内信一种消息推送方式。
                  <br>
                  4.设置最大Limit数后，所有的查询语句的查询结果都不会超过这个数值。
                  <br>
                  5.开启多级审核开关后,用户组将新增执行人角色，只有执行人角色的用户才能最终执行工单。关闭后执行人角色用户将全部更改为使用者
                  <br>
                  6.查询审核开关开启后，所有的查询都必须通过管理员同意才能进行。关闭则可自主查询
                  <br>
                  7.设置脱敏字段后，查询时如匹配到对应字段则该字段将只会以******显示
                </template>
              </Alert>
              <Button style="margin-left: 5%;width: 95%" type="primary" @click="save_upload">保存</Button>
            </Col>
          </Row>
        </Card>
      </Col>
    </Row>
  </div>
</template>

<script>
  import util from '../../libs/util'
  import axios from 'axios'

  export default {
    name: 'Setting',
    data () {
      return {
        inception: {
          host: '',
          port: '',
          user: '',
          password: '',
          back_host: '',
          back_port: '',
          back_user: '',
          back_password: ''
        },
        ldap: {
          type: '',
          host: '',
          sc: '',
          domain: '',
          user: '',
          password: ''
        },
        message: {
          webhook: '',
          smtp_host: '',
          smtp_port: '',
          user: '',
          password: '',
          to_user: '',
          mail: '',
          ding: ''
        },
        other: {
          sensitive_list: [],
          limit: '',
          con_room: [],
          foce: '',
          multi: '',
          query: '',
          sensitive: '',
          exclued_db_list: [],
          exclued_db: '',
          email_suffix_list: [],
          email_suffix: ''

        }
      }
    },
    methods: {
      handleAdd () {
        this.other.con_room.push(this.other.foce)
        this.other.foce = ''
      },
      handleAdd1 () {
        this.other.sensitive_list.push(this.other.sensitive)
        this.other.sensitive = ''
      },
      handleAdd_exclued_db () {
        this.other.exclued_db_list.push(this.other.exclued_db)
        this.other.exclued_db = ''
      },
      handleAddemail () {
        this.other.email_suffix_list.push(this.other.email_suffix)
        this.other.email_suffix = ''
      },
      handleClose2 (event, name) {
        const index = this.other.con_room.indexOf(name)
        this.other.con_room.splice(index, 1)
      },
      handleClose3 (event, name) {
        const index = this.other.sensitive_list.indexOf(name)
        this.other.sensitive_list.splice(index, 1)
      },
      handleClose_exclued_db (event, name) {
        const index = this.other.exclued_db_list.indexOf(name)
        this.other.exclued_db_list.splice(index, 1)
      },
      handleCloseemail (event, name) {
        const index = this.other.email_suffix_list.indexOf(name)
        this.other.email_suffix_list.splice(index, 1)
        console.log(this.other.email_suffix)
        console.log(this.other.email_suffix_list)
      },
      multi_switching (status) {
        this.other.multi = status
      },
      multi_query (status) {
        this.other.query = status
      },
      dingding_switching (status) {
        this.message.ding = status
      },
      mail_switching (status) {
        this.message.mail = status
      },
      ldap_test () {
        axios.put(`${util.url}/setting/1`, {
          'ldap': JSON.stringify(this.ldap)
        })
          .then(res => {
            util.notice(res.data)
          })
          .catch(error => {
            util.err_notice(this, error)
          })
      },
      dingding_test () {
        axios.put(`${util.url}/setting/2`, {
          'ding': this.message.webhook
        })
          .then(res => {
            util.notice(res.data)
          })
          .catch(error => {
            util.err_notice(this, error)
          })
      },
      mail_test () {
        axios.put(`${util.url}/setting/3`, {
          'mail': JSON.stringify(this.message)
        })
          .then(res => {
            util.notice(res.data)
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      save_upload () {
        axios.post(`${util.url}/setting/save`, {
          'inception': JSON.stringify(this.inception),
          'ldap': JSON.stringify(this.ldap),
          'message': JSON.stringify(this.message),
          'other': JSON.stringify(this.other)
        })
          .then(res => {
            util.notice(res.data)
          })
          .catch(error => {
            util.err_notice(this, error)
          })
      }
    },
    mounted () {
      axios.get(`${util.url}/setting/get`)
        .then(res => {
          if (res.data.other === 'refused') {
            this.$router.push({
              name: 'error_401'
            })
          } else {
            this.message = res.data.message
            this.message.mail ? this.message.mail = true : this.message.mail = false
            this.message.ding ? this.message.ding = true : this.message.ding = false
            this.inception = res.data.inception
            this.other = res.data.other
            this.other.multi ? this.other.multi = true : this.other.multi = false
            this.other.query ? this.other.query = true : this.other.query = false
            this.ldap = res.data.ldap
          }
        })
        .catch(error => {
          util.err_notice(error)
        })
    }
  }
</script>

<style scoped>

</style>
