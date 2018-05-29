<style>
  label{
    font-size: 30px;
  }
</style>

<template>
  <div>
    <Row>
      <Col span="24">
        <Card>
          <p slot="title">
            <Icon type="android-settings"></Icon>
            基础设置
          </p>
          <Row>
            <Col span="12">
          <Card>
            <p slot="title">Inception配置</p>
          <Form :label-width="120">
            <FormItem label="地址:">
              <Input placeholder="Inception ip地址"></Input>
            </FormItem>
            <FormItem label="端口:">
              <Input placeholder="Inception 端口"></Input>
            </FormItem>
            <FormItem label="用户名:">
              <Input placeholder="Inception 用户名"></Input>
            </FormItem>
            <FormItem label="密码:">
              <Input placeholder="Inception 密码(如未设置密码则不填写)" type="password"></Input>
            </FormItem>
            <FormItem label="备份库地址:">
              <Input placeholder="备份库 地址"></Input>
            </FormItem>
            <FormItem label="备份库端口:">
              <Input placeholder="备份库 端口"></Input>
            </FormItem>
            <FormItem label="备份库用户名:">
              <Input placeholder="备份库 用户名"></Input>
            </FormItem>
            <FormItem label="备份库密码:">
              <Input placeholder="备份库 密码(如未设置密码则不填写)" type="password"></Input>
            </FormItem>
          </Form>
          </Card>
            </Col>
            <Col span="12">
          <Card style="margin-left: 5%">
            <p slot="title">LDAP设置</p>
                <Form :label-width="120">
                  <FormItem label="LDAP认证类型:">
                    <Select>
                      <Option>域名认证</Option>
                      <Option>uid认证</Option>
                      <Option>cn认证</Option>
                    </Select>
                  </FormItem>
                  <FormItem label="服务地址:">
                    <Input placeholder="服务ip地址"></Input>
                  </FormItem>
                  <FormItem label="LDAP_SCBASE:">
                    <Input placeholder="LDAP dc 相关设置,采用域名认证可不填写"></Input>
                  </FormItem>
                  <FormItem label="LDAP_域名:">
                    <Input placeholder="LDAP Domain"></Input>
                  </FormItem>
                  <Button type="primary" @click="testlink()">LDAP测试</Button>
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
                    <Input placeholder="此webhook只用于查询工单,权限工单的消息推送。"></Input>
                  </FormItem>
                  <FormItem label="邮件SMTP服务地址:">
                    <Input placeholder="STMP服务 地址"></Input>
                  </FormItem>
                  <FormItem label="SMTP服务端口:">
                    <Input placeholder="STMP服务 端口"></Input>
                  </FormItem>
                  <FormItem label="邮件推送人用户名:">
                    <Input placeholder="推送人 用户名"></Input>
                  </FormItem>
                  <FormItem label="邮件推送人密码:">
                    <Input placeholder="推送人 密码" type="password"></Input>
                  </FormItem>
                  <Form-item label="email推送开关:">
                    <i-switch size="large" @on-change="mail_switching">
                      <span slot="open">开</span>
                      <span slot="close">关</span>
                    </i-switch>
                  </Form-item>
                  <Form-item label="钉钉推送开关:">
                    <i-switch size="large" @on-change="dingding_switching">
                      <span slot="open">开</span>
                      <span slot="close">关</span>
                    </i-switch>
                  </Form-item>
                  <Button type="primary" @click="testlink()">钉钉测试</Button>
                  <Button type="warning" @click="add()" style="margin-left: 5%">邮件测试</Button>
                </Form>
              </Card>
            </Col>
            <Col span="12">
              <Card style="margin-left: 5%">
                <p slot="title">其他</p>
                <Form :label-width="120">
                  <FormItem label="查询最大Limit限制:">
                    <Input placeholder="查询最大的Limit数。"></Input>
                  </FormItem>
                  <FormItem label="自定义机房:">
                    <Tag v-for="item in count" :key="item" :name="item" type="border" closable color="blue" @on-close="handleClose2" >{{ item }}</Tag>
                    <Button icon="ios-plus-empty" type="dashed" size="small" @click="handleAdd">添加机房</Button>
                  </FormItem>
                  <Form-item label="多级审核开关:">
                    <i-switch size="large" @on-change="mail_switching">
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
                  2.由于各个邮件服务提供商对于垃圾邮件过滤的机制不相同，所以可能会造成邮件无法接收的情况。请充分测试选择的邮件服务提供商服务是否稳定。
                  <br>
                  3.只有开启相应的消息推送开关后，消息推送才会开启。否则只有站内信一种消息推送方式。
                  <br>
                  4.设置最大Limit数后，所有的查询语句的查询结果都不会超过这个数值。
                </template>
              </Alert>
            </Col>
          </Row>
        </Card>
      </Col>
    </Row>
  </div>
</template>

<script>
  export default {
    name: 'Setting',
    data () {
      return {
        count: ['AWS', 'Aliyun', 'Own', 'Other']
      }
    },
    methods: {
      handleAdd () {
        if (this.count.length) {
          this.count.push(this.count[this.count.length - 1] + 1);
        } else {
          this.count.push(0);
        }
      },
      handleClose2 (event, name) {
        const index = this.count.indexOf(name);
        this.count.splice(index, 1);
      }
    }
  }
</script>

<style scoped>

</style>
